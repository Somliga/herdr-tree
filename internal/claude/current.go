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

// Current resolves the pane's Claude session. The registry is authoritative;
// Herdr's agent_session value is only a tie-breaker, because it records the
// last session id seen in the pane including headless ones.
func Current(p adapter.Pane) (string, error) {
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
		if p.CWD != "" && e.CWD != p.CWD {
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
