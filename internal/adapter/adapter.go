// Package adapter defines the agent-neutral types and the interface every
// agent adapter implements. Nothing in this package may know about Claude.
package adapter

import "time"

// Node is one conversation turn.
type Node struct {
	ID    string // stable id of the turn within its session
	Title string // single-line label, already truncated
	At    time.Time
}

// Session is one agent session: a linear path of turns.
type Session struct {
	ID      string
	CWD     string
	Path    string    // where the transcript file was found
	Title   string
	Updated time.Time
	Nodes   []Node // ordered root -> leaf
	Live    bool   // a process currently holds it
	Broken  bool   // transcript present but unreadable
}

// Pane is what Herdr reports about the invoking pane.
type Pane struct {
	ID             string
	CWD            string
	Agent          string // agent kind Herdr detected, e.g. "claude"
	AgentSessionID string // Herdr's belief about the session; a hint only
}

// Adapter is the whole agent-specific surface.
type Adapter interface {
	Name() string
	Discover(repoRoot string) ([]Session, error)
	Current(p Pane) (sessionID string, err error)
	// Preview reports what a graft at atNode would carry, for the
	// confirmation dialog. Computed without writing anything.
	Preview(src Session, atNode string) (turns, entries int, size int64, err error)
	Branch(src Session, atNode, dstCWD string) (newSessionID string, err error)
	Resume(sessionID, cwd string) error
}
