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
//
// The synthetic "Continue from where you left off." turn is kept: it is
// real conversation content, and only the tree view hides it.
func Select(es []Entry, atNode string) (map[string]bool, error) {
	byUUID := make(map[string]Entry, len(es))
	for _, e := range es {
		if u := e.UUID(); u != "" {
			byUUID[u] = e
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

	// Rule 2.
	reqs := map[string]bool{}
	for u := range keep {
		if e := byUUID[u]; e.Type() == "assistant" && e.RequestID() != "" {
			reqs[e.RequestID()] = true
		}
	}
	for _, e := range es {
		if e.Type() == "assistant" && e.RequestID() != "" && reqs[e.RequestID()] {
			if u := e.UUID(); u != "" {
				keep[u] = true
			}
		}
	}

	// Rule 3.
	for _, e := range es {
		if e.Type() == "attachment" && keep[e.ParentUUID()] {
			if u := e.UUID(); u != "" {
				keep[u] = true
			}
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

// droppedTypes are session-scoped bookkeeping entries that must not be
// copied into a new session.
var droppedTypes = map[string]bool{
	"last-prompt": true,
	"ai-title":    true,
	"cost-state":  true,
}

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
		return nil
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
		if u != "" && !keep[u] {
			continue
		}
		if u == "" && droppedTypes[e.Type()] {
			continue
		}
		// Copy the map so the source entries stay untouched.
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
