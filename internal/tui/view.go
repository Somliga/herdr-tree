package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// renderRow draws one line. Rendering is deliberately plain text so it can
// be asserted in tests; View applies the StyleKey it returns.
func renderRow(r Row, selected bool, currentSession string, width int) (string, StyleKey) {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", r.Depth))

	if r.InRange {
		b.WriteString("┃ ")
	}
	if r.Node.Broken {
		b.WriteString("⚠ ")
	}
	if r.Node.Grafted {
		b.WriteString("↳ ")
	}
	if r.Node.IsSessionRoot {
		b.WriteString(shortID(r.Node.SessionID) + "  ")
	}
	if r.HasChildren && r.Folded {
		b.WriteString("▸ ")
	}


	if r.Node.Label != "" {
		b.WriteString("★ " + r.Node.Label + "  ")
	}
	switch r.Node.Node.Kind {
	case adapter.KindAssistant:
		b.WriteString("assistant: ")
	case adapter.KindToolCall:
		// the label already carries its own brackets
	case adapter.KindSummaryImport, adapter.KindSummaryCompaction:
		// No prefix here: the title IS the seed's first line, which begins
		// with ⤶ by construction — Classify only assigns these kinds when
		// that prefix is present, and GraftSeeded refuses a seed without it.
		// Prepending another produced "⤶ ⤶ summary of …".
	default:
		if !r.Node.IsSessionRoot {
			b.WriteString("user: ")
		}
	}
	title := r.Node.Node.Title
	if title == "" && r.Node.Broken {
		title = "transcript unreadable — metadata only"
	}
	b.WriteString(title)

	currentTip := r.Node.SessionID != "" && r.Node.SessionID == currentSession && r.Node.IsSessionLeaf
	if currentTip {
		b.WriteString("   ● current")
	}
	if r.Folded && r.HasChildren {
		b.WriteString(fmt.Sprintf("  (%d)", r.BodyCount))
	}
	line := b.String()
	if width > 0 && len([]rune(line)) > width {
		line = string([]rune(line)[:width-1]) + "…"
	}
	key := styleFor(r.Node, currentTip)
	// A range in progress is the thing the user is actively manipulating, so
	// it outranks the row's ordinary kind colour and even the current-tip
	// marker — but not Broken, which is data integrity and always wins.
	if r.InRange && key != StyleBroken {
		key = StyleRange
	}
	return line, key
}

// confirmText is the branch confirmation, which is where the user is told
// exactly what a graft copies and where it will open.
func confirmText(n *tree.Node, turns, entries int, size int64, dstCWD string) string {
	return fmt.Sprintf(
		"Continue from:  %q\n\nThis starts a NEW session carrying %d turn(s) · %d entries · %s.\nThe original is untouched.\n\nOpens: split right, unfocused in %s\n\n[enter] continue   [esc] cancel",
		n.Node.Title, turns, entries, humanBytes(size), dstCWD)
}

type uiModel struct {
	m        *Model
	a        adapter.Adapter
	st       *store.Store
	repoRoot string
	current  string
	width    int
	height   int
	confirm  string
	status   string
	busy     string // non-empty while an adapter call is in flight
	quitting bool

	labelling *tree.Node // non-nil while typing a label
	labelText string

	roots    []*tree.Node // the whole forest
	scopeAll bool         // false: just the current session's tree
}

// rebuild reapplies the scope, keeping the selected node where it still
// exists so toggling scope does not lose your place.
func (u *uiModel) rebuild() {
	was := u.m.Selected()
	roots := u.roots
	if !u.scopeAll {
		if scoped := ScopeTo(u.roots, u.current); scoped != nil {
			roots = scoped
		}
	}
	u.m = New(roots)
	u.m.SetTrunk(tree.Trunk(u.roots, u.current))
	if was == nil {
		return
	}
	for i, r := range u.m.Rows() {
		if r.Node == was {
			u.m.Cursor = i
			return
		}
	}
}

// actionDoneMsg carries the result of an adapter call back onto the update
// loop. `quit` is set only when the action succeeded — a failure must leave
// the overlay open, because Bubble Tea paints its final frame into the alt
// screen and then discards it on exit, so a message shown while quitting is
// never actually read by anyone.
type actionDoneMsg struct {
	status string
	quit   bool
}

// resumeCmd and branchCmd run OFF the update loop.
//
// herdr's `agent start` waits for the agent to become ready and is bounded at
// 45 seconds. Doing that inside Update freezes every keystroke for the whole
// duration with no feedback and no way to cancel, because Bubble Tea handles
// one message at a time. As a tea.Cmd the work happens on its own goroutine
// and the overlay keeps rendering.
//
// dstCWD is where a pane for this node should open. Normally the session's
// own directory, so a worktree session reopens in its worktree. But that
// directory can be gone — a removed worktree still shows in the tree by
// design — and opening a pane there fails after the graft has already been
// written. Fall back to the repo root, which exists by construction.
func (u uiModel) dstCWD(n *tree.Node) string {
	if n.SessionCWD != "" {
		if fi, err := os.Stat(n.SessionCWD); err == nil && fi.IsDir() {
			return n.SessionCWD
		}
	}
	return u.repoRoot
}

func resumeCmd(a adapter.Adapter, n *tree.Node, dst string) tea.Cmd {
	return func() tea.Msg {
		if err := a.Resume(n.SessionID, dst); err != nil {
			return actionDoneMsg{status: "could not open session: " + err.Error()}
		}
		return actionDoneMsg{status: "opened " + shortID(n.SessionID), quit: true}
	}
}

func branchCmd(a adapter.Adapter, st *store.Store, n *tree.Node, dst string) tea.Cmd {
	return func() tea.Msg {
		src := adapter.Session{ID: n.SessionID, CWD: n.SessionCWD, Path: n.SessionPath}
		sid, err := a.Branch(src, n.Node.ID, dst)
		if err != nil {
			return actionDoneMsg{status: "branch failed: " + err.Error()}
		}
		// Record the edge before resuming: the transcript now exists, so the
		// branch must survive even if opening it fails.
		st.Add(sid, store.Branch{
			GraftedFrom: store.From{SessionID: n.SessionID, Node: n.Node.ID},
			Title:       n.Node.Title,
			CreatedAt:   time.Now().UTC(),
		})
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: "branched " + shortID(sid) + ", but the tree was not saved: " + err.Error()}
		}
		if err := a.Resume(sid, dst); err != nil {
			return actionDoneMsg{status: "branched " + shortID(sid) + ", but it did not open: " + err.Error()}
		}
		return actionDoneMsg{status: "branched " + shortID(sid), quit: true}
	}
}

func (u uiModel) Init() tea.Cmd { return nil }

func (u uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
	case actionDoneMsg:
		u.busy = ""
		u.status = msg.status
		if msg.quit {
			u.quitting = true
			return u, tea.Quit
		}
		return u, nil
	case tea.KeyMsg:
		if u.busy != "" {
			// An adapter call is in flight. Swallow input rather than queueing
			// a second one, but never trap the user.
			if msg.String() == "ctrl+c" {
				u.quitting = true
				return u, tea.Quit
			}
			return u, nil
		}
		if u.labelling != nil {
			switch msg.Type {
			case tea.KeyEnter:
				n := u.labelling
				u.st.SetLabel(n.SessionID, n.Node.ID, strings.TrimSpace(u.labelText))
				n.Label = strings.TrimSpace(u.labelText)
				if err := u.st.Save(); err != nil {
					u.status = "label not saved: " + err.Error()
				}
				u.labelling, u.labelText = nil, ""
			case tea.KeyEsc:
				u.labelling, u.labelText = nil, ""
			case tea.KeyBackspace:
				if r := []rune(u.labelText); len(r) > 0 {
					u.labelText = string(r[:len(r)-1])
				}
			case tea.KeyRunes, tea.KeySpace:
				u.labelText += msg.String()
			}
			return u, nil
		}
		if u.confirm != "" {
			switch msg.String() {
			case "enter":
				n := u.m.Selected()
				u.confirm = ""
				if n == nil {
					return u, nil
				}
				u.busy = "branching…"
				return u, branchCmd(u.a, u.st, n, u.dstCWD(n))
			case "esc", "q":
				u.confirm = ""
			}
			return u, nil
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			u.quitting = true
			return u, tea.Quit
		case "up", "k":
			u.m.Up()
		case "down", "j":
			u.m.Down()
		case "left", "h":
			u.m.Fold()
		case "right", "l":
			u.m.Unfold()
		case "enter":
			n := u.m.Selected()
			if n == nil || n.Broken {
				return u, nil
			}
			if n.IsSessionLeaf {
				// Already the tip: continuing means resuming, and nothing is
				// written. No confirmation, because there is nothing to confirm.
				u.busy = "opening session…"
				return u, resumeCmd(u.a, n, u.dstCWD(n))
			}
			src := adapter.Session{ID: n.SessionID, CWD: n.SessionCWD, Path: n.SessionPath}
			turns, entries, size, err := u.a.Preview(src, n.Node.ID)
			if err != nil {
				u.status = "cannot continue from here: " + err.Error()
				return u, nil
			}
			u.confirm = confirmText(n, turns, entries, size, u.dstCWD(n))
		case "a":
			u.scopeAll = !u.scopeAll
			u.rebuild()
		case "f":
			u.m.CycleFilter()
		case "L":
			n := u.m.Selected()
			if n == nil || n.Node.ID == "" {
				return u, nil
			}
			u.labelling = n
			u.labelText = n.Label
		}
	}
	return u, nil
}

func (u uiModel) View() string {
	if u.quitting {
		return ""
	}
	if u.labelling != nil {
		return fmt.Sprintf("Label this turn:  %s\n\n  %q\n\n[enter] save   [esc] cancel   (empty clears)\n",
			u.labelText, u.labelling.Node.Title)
	}
	if u.confirm != "" {
		return u.confirm + "\n"
	}
	var b strings.Builder
	if len(u.m.Rows()) == 0 {
		b.WriteString("No Claude sessions found for this directory.\n")
	}
	height := u.height - 4 // header, blank, footer, status
	if height < 5 {
		height = 5
	}
	rows, start, total := u.m.Window(height)
	for i, r := range rows {
		marker := "  "
		if start+i == u.m.Cursor {
			marker = "> "
		}
		text, key := renderRow(r, start+i == u.m.Cursor, u.current, u.width-2)
		b.WriteString(marker + render(key, text) + "\n")
	}
	if total > 0 {
		b.WriteString(fmt.Sprintf("\n(%d/%d)\n", u.m.Cursor+1, total))
	} else {
		b.WriteString("\n")
	}
	scope := "this session"
	if u.scopeAll {
		scope = "all sessions"
	}
	b.WriteString(fmt.Sprintf("↑↓ move  ←→ fold  ⏎ continue from here  L label  a scope:%s  f filter:%s  esc close\n", scope, u.m.Filter))
	if u.busy != "" {
		b.WriteString(u.busy + "\n")
	}
	if u.status != "" {
		b.WriteString(u.status + "\n")
	}
	return b.String()
}

// Run starts the overlay.
func Run(a adapter.Adapter, repoRoot string, st *store.Store, sessions []adapter.Session, current string) error {
	roots := tree.Build(sessions, st)
	u := uiModel{m: New(roots), a: a, st: st, repoRoot: repoRoot, current: current, roots: roots}
	u.rebuild() // start scoped to the current session
	_, err := tea.NewProgram(u, tea.WithAltScreen()).Run()
	return err
}
