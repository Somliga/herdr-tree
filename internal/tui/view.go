package tui

import (
	"fmt"
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
// be asserted in tests.
func renderRow(r Row, selected bool, currentSession string, width int) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", r.Depth))

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

	title := r.Node.Node.Title
	if title == "" && r.Node.Broken {
		title = "transcript unreadable — metadata only"
	}
	b.WriteString(title)

	if r.Node.SessionID != "" && r.Node.SessionID == currentSession {
		b.WriteString("   ● current")
	}
	line := b.String()
	if width > 0 && len([]rune(line)) > width {
		line = string([]rune(line)[:width-1]) + "…"
	}
	return line
}

// confirmText is the branch confirmation, which is where the user is told
// exactly what a graft copies.
func confirmText(n *tree.Node, turns, entries int, size int64, dstCWD string) string {
	return fmt.Sprintf(
		"Branch from:  %q\nCarries:      %d turns · %d entries · %s\nOpens:        split right, unfocused in %s\n\n[enter] branch   [esc] cancel",
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
	quitting bool
}

func (u uiModel) Init() tea.Cmd { return nil }

func (u uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if u.confirm != "" {
			switch msg.String() {
			case "enter":
				u.status = u.doBranch()
				u.confirm = ""
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
			u.status = u.doResume()
			return u, tea.Quit
		case "b":
			if n := u.m.Selected(); n != nil && !n.Broken {
				src := adapter.Session{ID: n.SessionID, CWD: n.SessionCWD, Path: n.SessionPath}
				turns, entries, size, err := u.a.Preview(src, n.Node.ID)
				if err != nil {
					u.status = "cannot branch here: " + err.Error()
				} else {
					u.confirm = confirmText(n, turns, entries, size, n.SessionCWD)
				}
			}
		}
	}
	return u, nil
}

func (u *uiModel) doResume() string {
	n := u.m.Selected()
	if n == nil {
		return ""
	}
	if err := u.a.Resume(n.SessionID, n.SessionCWD); err != nil {
		return "could not open session: " + err.Error()
	}
	return "opened " + shortID(n.SessionID)
}

func (u *uiModel) doBranch() string {
	n := u.m.Selected()
	if n == nil {
		return ""
	}
	src := adapter.Session{ID: n.SessionID, CWD: n.SessionCWD, Path: n.SessionPath}
	sid, err := u.a.Branch(src, n.Node.ID, n.SessionCWD)
	if err != nil {
		return "branch failed: " + err.Error()
	}
	u.st.Add(sid, store.Branch{
		GraftedFrom: store.From{SessionID: n.SessionID, Node: n.Node.ID},
		Title:       n.Node.Title,
		CreatedAt:   time.Now().UTC(),
	})
	if err := u.st.Save(); err != nil {
		return "branched " + shortID(sid) + ", but the tree was not saved: " + err.Error()
	}
	if err := u.a.Resume(sid, n.SessionCWD); err != nil {
		return "branched " + shortID(sid) + ", but it did not open: " + err.Error()
	}
	return "branched " + shortID(sid)
}

func (u uiModel) View() string {
	if u.quitting {
		return ""
	}
	if u.confirm != "" {
		return u.confirm + "\n"
	}
	var b strings.Builder
	rows := u.m.Rows()
	if len(rows) == 0 {
		b.WriteString("No Claude sessions found for this directory.\n")
	}
	for i, r := range rows {
		marker := "  "
		if i == u.m.Cursor {
			marker = "> "
		}
		b.WriteString(marker + renderRow(r, i == u.m.Cursor, u.current, u.width-2) + "\n")
	}
	b.WriteString("\n↑↓ move  ←→ fold  ⏎ open  b branch  esc close\n")
	if u.status != "" {
		b.WriteString(u.status + "\n")
	}
	return b.String()
}

// Run starts the overlay.
func Run(a adapter.Adapter, repoRoot string, st *store.Store, sessions []adapter.Session, current string) error {
	roots := tree.Build(sessions, st)
	u := uiModel{m: New(roots), a: a, st: st, repoRoot: repoRoot, current: current}
	_, err := tea.NewProgram(u, tea.WithAltScreen()).Run()
	return err
}
