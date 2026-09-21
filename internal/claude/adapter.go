package claude

import (
	"fmt"
	"path/filepath"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/herdr"
)

type claudeAdapter struct{}

// New returns the Claude Code adapter.
func New() adapter.Adapter { return claudeAdapter{} }

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Discover(repoRoot string) ([]adapter.Session, error) {
	return Discover(repoRoot)
}

func (claudeAdapter) Current(p adapter.Pane) (string, error) { return Current(p) }

// TranscriptPath is where Claude Code will look for a session started in
// cwd. Use it for a file about to be WRITTEN. To READ an existing session,
// use Session.Path, which is where the file was actually found — the two
// disagree for a session that relocated into a worktree.
func TranscriptPath(sessionID, cwd string) string {
	return filepath.Join(ProjectsDir(), SlugFor(cwd), sessionID+".jsonl")
}

// sourcePath prefers the discovered path and falls back to reconstruction
// for a Session built by hand.
func sourcePath(src adapter.Session) string {
	if src.Path != "" {
		return src.Path
	}
	return TranscriptPath(src.ID, src.CWD)
}

// Preview reports what a graft at atNode would carry, without writing.
func (claudeAdapter) Preview(src adapter.Session, atNode string) (turns, entries int, size int64, err error) {
	es, _, err := ParseFile(sourcePath(src))
	if err != nil {
		return 0, 0, 0, err
	}
	keep, err := Select(es, atNode)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range es {
		u := e.UUID()
		if u == "" || !keep[u] {
			continue
		}
		entries++
		if IsPrompt(e) {
			turns++
		}
		if b, merr := Marshal(e); merr == nil {
			size += int64(len(b)) + 1
		}
	}
	return turns, entries, size, nil
}

func (claudeAdapter) Branch(src adapter.Session, atNode, dstCWD string) (string, error) {
	sid, _, err := Graft(sourcePath(src), atNode, dstCWD)
	if err != nil {
		return "", err
	}
	return sid, nil
}

// agentName builds a Herdr agent name for a session in a pane.
//
// It includes the pane because Herdr requires live agent names to be unique,
// and a name derived from the session alone collides the moment the same
// session is opened twice — a retry after a failure, or a session already
// open elsewhere. Resume always creates a fresh pane, so the pane id makes
// the name unique in practice.
//
// Herdr accepts [a-z][a-z0-9_-]{0,31}, so everything is lowercased, anything
// outside that set is dropped, and the result is capped.
func agentName(sessionID, paneID string) string {
	keep := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	n := "tree-" + keep(strings.SplitN(sessionID, "-", 2)[0]) + "-" + keep(paneID)
	if len(n) > 32 {
		n = n[:32]
	}
	return strings.TrimRight(n, "-")
}

// Resume asks Herdr for a pane and starts Claude in it. Nothing is spawned
// by this process.
func (claudeAdapter) Resume(sessionID, cwd string) error {
	paneID, err := herdr.Split(cwd)
	if err != nil {
		return fmt.Errorf("open pane: %w", err)
	}
	if err := herdr.AgentStart(agentName(sessionID, paneID), paneID, sessionID); err != nil {
		// Say that the pane exists, so the empty pane the user is now looking
		// at is explained rather than mysterious.
		return fmt.Errorf("opened pane %s but could not start claude in it: %w", paneID, err)
	}
	return nil
}
