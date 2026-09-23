// Package adapter defines the agent-neutral types and the interface every
// agent adapter implements. Nothing in this package may know about Claude.
package adapter

import "time"

// Kind is what produced an entry. Universal across agents: a human typed it,
// the model said it, or the model called a tool.
type Kind int

const (
	KindHuman Kind = iota
	KindAssistant
	KindToolCall
	KindSummaryImport     // knowledge folded in from a branch
	KindSummaryCompaction // a range of this line's own turns, shortened
)

func (k Kind) String() string {
	switch k {
	case KindAssistant:
		return "assistant"
	case KindToolCall:
		return "tool"
	case KindSummaryImport:
		return "import"
	case KindSummaryCompaction:
		return "compaction"
	default:
		return "user"
	}
}

// Node is one conversation turn.
type Node struct {
	ID    string // stable id of the turn within its session
	Title string // single-line label, already truncated
	Kind  Kind
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
	BranchSeeded(src Session, atNode, dstCWD, seed string) (newSessionID string, err error)
	Resume(sessionID, cwd string, focus bool) error
	Summarise(src Session, fromTurn, toTurn string) (string, error)
	// Widen reports what a range covers once widened to whole turns.
	Widen(src Session, fromNode, toNode string) (Span, error)
	// Splice writes a new session with e applied to src's current line. The
	// source is never modified.
	Splice(src Session, e Edit, dstCWD string) (Spliced, error)
}

// Edit is one change to a line's context. From..To names a range of entries,
// widened to whole turns, to remove; Seed, if set, takes its place. After,
// used instead of a range, names an entry after whose turn Seed is inserted
// and nothing is removed. A range with no Seed is a cut.
type Edit struct {
	From, To string
	After    string
	Seed     string
}

// Span is a selection widened to whole turns.
type Span struct {
	First, Last int    // turn numbers; 0 is the preamble before the first prompt
	End         string // the last entry of turn Last: where a summary stops reading
}

// Spliced is what a splice wrote.
type Spliced struct {
	SessionID string
	Removed   int    // whole turns removed, not counting the preamble
	After     string // the first entry after the edit, "" when nothing follows
}
