package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"herdr-tree/internal/adapter"
)

var (
	ErrNoSession        = errors.New("no interactive Claude session for this pane")
	ErrAmbiguousSession = errors.New("several interactive Claude sessions match this pane")
	ErrUnknownCWD       = errors.New("pane reported no working directory")
)

// SessionsDir is Claude Code's live process registry.
func SessionsDir() string {
	if d := os.Getenv("CLAUDE_SESSIONS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

type regEntry struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
}

// samePath compares two directories by identity rather than by spelling.
// Herdr and Claude Code can report the same directory differently — one
// through a symlink, one with a trailing slash — and a plain string compare
// would drop a real match to zero and report no session at all.
func samePath(a, b string) bool { return normPath(a) == normPath(b) }

func normPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p // may not exist any more; the cleaned spelling is the best we have
}

// Current resolves the pane's Claude session. The registry is authoritative;
// Herdr's agent_session value is only a tie-breaker, because it records the
// last session id seen in the pane including headless ones.
func Current(p adapter.Pane) (string, error) {
	if p.CWD == "" {
		// Without a pane directory there is nothing to match against, and
		// matching everything would silently return whichever session happens
		// to be the only one running. Missing evidence is not evidence that
		// any session will do.
		return "", ErrUnknownCWD
	}
	paths, err := filepath.Glob(filepath.Join(SessionsDir(), "*.json"))
	if err != nil {
		return "", err
	}
	var matches []string
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e regEntry
		if json.Unmarshal(b, &e) != nil {
			continue
		}
		if e.Kind != "interactive" || e.SessionID == "" {
			continue
		}
		if !samePath(e.CWD, p.CWD) {
			continue
		}
		matches = append(matches, e.SessionID)
	}

	switch len(matches) {
	case 0:
		return "", ErrNoSession
	case 1:
		return matches[0], nil
	}
	for _, m := range matches {
		if m == p.AgentSessionID {
			return m, nil
		}
	}
	return "", ErrAmbiguousSession
}
