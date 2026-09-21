package claude

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNodeNotFound means the requested graft point is not in the transcript.
var ErrNodeNotFound = errors.New("graft node not found in transcript")

// Select returns the set of entry uuids a graft at atNode must keep.
//
// Rule 1: the ancestor chain of atNode.
// Rule 2: assistant entries sharing a kept entry's requestId, because one
//         assistant response is written as several entries, one per block.
// Rule 3: attachment entries whose parent is kept.
// Rule 4: tool_result entries whose parent is kept.
//
// Rule 4 is not symmetry for its own sake. Claude Code writes a parallel tool
// call as a CHAIN of assistant entries, one per tool_use block, and then one
// tool_result per tool, each parented to its own tool_use. Only one of those
// results lies on the linear parentUuid chain; the rest are siblings. Without
// this rule the graft keeps every tool_use (they are all ancestors) while
// dropping the sibling results, producing assistant turns whose tool_use
// blocks have no answer — a shape Claude Code never writes and the Messages
// API rejects. Measured across every graft point in 103 real transcripts on
// the development machine: 48.3% of grafts orphaned at least one tool_use,
// 12340 blocks in total, against an orphan rate of 0.03% in the source files.
//
// The synthetic "Continue from where you left off." turn is kept: it is
// real conversation content, and only the tree view hides it.
func Select(es []Entry, atNode string) (map[string]bool, error) {
	byUUID := make(map[string]Entry, len(es))
	order := make(map[string]int, len(es))
	for i, e := range es {
		if u := e.UUID(); u != "" {
			byUUID[u] = e
			order[u] = i
		}
	}
	if _, ok := byUUID[atNode]; !ok {
		return nil, ErrNodeNotFound
	}

	keep := map[string]bool{}

	// Rule 1.
	for cur := atNode; cur != ""; {
		e, ok := byUUID[cur]
		if !ok || keep[cur] {
			break // missing parent or a cycle: stop, do not loop forever
		}
		keep[cur] = true
		cur = e.ParentUUID()
	}

	// Rule 2, bounded to entries at or before the graft point.
	//
	// A requestId identifies a whole assistant turn, so an unbounded sweep can
	// pull in entries written AFTER the branch — importing work the user chose
	// to prune. Measured across every graft point in 103 real transcripts: 14
	// assistant and 14 user entries were captured this way. Small, but it is
	// content from a branch the user deliberately left behind, which is the
	// opposite of what this function is for.
	//
	// The bound is deliberately on rule 2 only. Rule 3's attachments are
	// system-reminders injected WITH a kept prompt and hang off it as
	// children, so they are legitimately part of that turn even though they
	// are written later — 2828 of them across the same corpus.
	at, ok := order[atNode]
	if !ok {
		return nil, ErrNodeNotFound
	}
	reqs := map[string]bool{}
	for u := range keep {
		if e := byUUID[u]; e.Type() == "assistant" && e.RequestID() != "" {
			reqs[e.RequestID()] = true
		}
	}
	for _, e := range es {
		if e.Type() == "assistant" && e.RequestID() != "" && reqs[e.RequestID()] {
			if u := e.UUID(); u != "" && order[u] <= at {
				keep[u] = true
			}
		}
	}

	// Rules 3 and 4, iterated to a fixpoint.
	//
	// A single file-order pass happens to work for attachments only because
	// Claude Code writes them after their parent. That is an accident of the
	// format, not a guarantee, and a tool_result recovered by rule 4 can
	// itself have attachment children. Looping until nothing new is added
	// removes the dependency on write order entirely. The corpus converges in
	// two passes; the loop is bounded by the entry count regardless.
	for {
		added := false
		for _, e := range es {
			u := e.UUID()
			if u == "" || keep[u] || !keep[e.ParentUUID()] {
				continue
			}
			if e.Type() == "attachment" || (e.Type() == "user" && e.IsToolResult()) {
				keep[u] = true
				added = true
			}
		}
		if !added {
			break
		}
	}

	return keep, nil
}

// ErrUnsupportedVersion means the transcript was written by a Claude Code
// whose format this adapter has not been verified against. Refusing is
// correct: a wrong graft produces a plausible session with wrong history.
var ErrUnsupportedVersion = errors.New("unsupported Claude Code transcript version")

// ErrPartialTranscript means some lines of the source transcript could not be
// parsed, so its parentUuid chain cannot be trusted.
var ErrPartialTranscript = errors.New("source transcript has unparseable lines")

// verifiedMajorMinor is the format this adapter was validated against.
const verifiedMajorMinor = "2.1"

func checkVersion(es []Entry) error {
	for _, e := range es {
		v := e.Version()
		if v == "" {
			continue
		}
		parts := strings.SplitN(v, ".", 3)
		if len(parts) < 2 || parts[0]+"."+parts[1] != verifiedMajorMinor {
			return ErrUnsupportedVersion
		}
		// Keep scanning. A transcript can span a Claude Code upgrade, and
		// returning on the first versioned entry would accept a file whose
		// later entries use a format this adapter has never been validated
		// against.
	}
	return nil // no version stamped anywhere: nothing to disagree with
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Graft writes a new transcript containing only the ancestor chain of
// atNode, as a fresh session rooted at dstCWD, and returns its id and path.
// The source transcript is never modified.
func Graft(srcPath, atNode, dstCWD string) (newSessionID, dstPath string, err error) {
	es, skipped, err := ParseFile(srcPath)
	if err != nil {
		return "", "", err
	}
	if skipped > 0 {
		// A dropped line can break the parentUuid chain, which would produce
		// a graft that looks fine and carries the wrong history.
		return "", "", ErrPartialTranscript
	}
	if err := checkVersion(es); err != nil {
		return "", "", err
	}
	keep, err := Select(es, atNode)
	if err != nil {
		return "", "", err
	}

	newSessionID, err = newUUIDv4()
	if err != nil {
		return "", "", err
	}

	dstDir := filepath.Join(ProjectsDir(), SlugFor(dstCWD))
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return "", "", err
	}
	dstPath = filepath.Join(dstDir, newSessionID+".jsonl")

	var buf []byte
	for _, e := range es {
		u := e.UUID()
		if u == "" {
			// Every uuid-less entry is session-scoped bookkeeping: mode,
			// permission-mode, atis-latch, queue-operation (which carries
			// queued prompt TEXT), relocated and worktree-state (the old
			// working directory), file-history-snapshot/delta, the artifact
			// ledgers (which carry an accountUuid), last-prompt, ai-title,
			// cost-state. All of it belongs to the session being branched
			// FROM. A denylist here is default-allow and silently leaks
			// whatever entry types Claude Code adds next, so drop the lot.
			// Verified empirically: a graft containing no bookkeeping at all
			// resumes correctly, and Claude Code writes fresh entries of its
			// own on resume.
			continue
		}
		if !keep[u] {
			continue
		}
		// Shallow copy, so the source entries stay untouched. Only top-level
		// keys are rewritten below; nested maps (message, attachment,
		// toolUseResult) still alias the source, so never mutate inside them.
		m := make(map[string]any, len(e.Raw))
		for k, v := range e.Raw {
			m[k] = v
		}
		for _, k := range []string{"sessionId", "session_id"} {
			if _, ok := m[k]; ok {
				m[k] = newSessionID
			}
		}
		if _, ok := m["cwd"]; ok {
			m["cwd"] = dstCWD
		}
		b, err := Marshal(Entry{Raw: m})
		if err != nil {
			return "", "", err
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}

	// The leaf pointer Claude Code writes. Proven not sufficient on its own
	// to truncate history — the pruning above is what does that — but it is
	// what the format contains, so write it.
	leaf, err := Marshal(Entry{Raw: map[string]any{
		"type": "last-prompt", "leafUuid": atNode, "sessionId": newSessionID,
	}})
	if err != nil {
		return "", "", err
	}
	buf = append(buf, leaf...)
	buf = append(buf, '\n')

	tmp, err := os.CreateTemp(dstDir, ".graft-*")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", "", err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return "", "", err
	}
	return newSessionID, dstPath, nil
}
