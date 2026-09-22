package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

func TestRenderRowShowsTitleAndIndent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n2", Title: "i want to discuss the weather"}, SessionID: "82cb69f2-x"}
	got := renderRow(Row{Node: n, Depth: 1}, false, "", 80)
	if !strings.Contains(got, "i want to discuss the weather") {
		t.Fatalf("title missing: %q", got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("depth not indented: %q", got)
	}
}

func TestRenderRowMarksCurrent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a", IsSessionLeaf: true}
	got := renderRow(Row{Node: n}, false, "sid-a", 80)
	if !strings.Contains(got, "● current") {
		t.Fatalf("current marker missing: %q", got)
	}
}

func TestRenderRowShowsSessionIdOnRoots(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "n1", Title: "x"},
		SessionID: "82cb69f2-e18b-4f86-874a-89e93139324a", IsSessionRoot: true,
	}
	got := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "82cb69f2") {
		t.Fatalf("short session id missing: %q", got)
	}
	if strings.Contains(got, "e18b") {
		t.Fatalf("full uuid should not be shown: %q", got)
	}
}

func TestRenderRowMarksBroken(t *testing.T) {
	n := &tree.Node{SessionID: "sid", IsSessionRoot: true, Broken: true}
	got := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("broken marker missing: %q", got)
	}
}

func TestRenderRowMarksGraft(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "m1", Title: "alt"},
		SessionID: "f2af34a4-x", IsSessionRoot: true, Grafted: true,
	}
	got := renderRow(Row{Node: n, Depth: 2}, false, "", 80)
	if !strings.Contains(got, "↳") {
		t.Fatalf("graft marker missing: %q", got)
	}
}

// fakeAdapter lets the update loop be tested without Herdr or Claude.
type fakeAdapter struct{ resumeErr error }

func (f fakeAdapter) Name() string                               { return "fake" }
func (f fakeAdapter) Discover(string) ([]adapter.Session, error) { return nil, nil }
func (f fakeAdapter) Current(adapter.Pane) (string, error)       { return "", nil }
func (f fakeAdapter) Preview(adapter.Session, string) (int, int, int64, error) {
	return 1, 2, 3, nil
}
func (f fakeAdapter) Branch(adapter.Session, string, string) (string, error) { return "new-sid", nil }
func (f fakeAdapter) Resume(string, string) error                            { return f.resumeErr }

func TestFailedResumeKeepsTheOverlayOpen(t *testing.T) {
	// Bubble Tea discards its final frame when leaving the alt screen, so a
	// status set while quitting is never read. A failure must not quit.
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{resumeErr: errors.New("pane split refused")}}

	cmd := resumeCmd(u.a, n, "sid-a-cwd")
	msg, ok := cmd().(actionDoneMsg)
	if !ok {
		t.Fatalf("want actionDoneMsg, got %T", cmd())
	}
	if msg.quit {
		t.Fatal("a failed resume must not quit: the message would never be seen")
	}
	if !strings.Contains(msg.status, "pane split refused") {
		t.Fatalf("status does not carry the cause: %q", msg.status)
	}

	after, _ := u.Update(msg)
	got := after.(uiModel)
	if got.quitting {
		t.Fatal("model marked quitting after a failed resume")
	}
	if !strings.Contains(got.View(), "pane split refused") {
		t.Fatalf("the error is not rendered:\n%s", got.View())
	}
}

func TestSuccessfulResumeQuits(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}}

	msg := resumeCmd(u.a, n, "sid-a-cwd")().(actionDoneMsg)
	if !msg.quit {
		t.Fatal("a successful resume should close the overlay")
	}
	after, cmd := u.Update(msg)
	if !after.(uiModel).quitting {
		t.Fatal("want quitting set")
	}
	if cmd == nil {
		t.Fatal("want a quit command")
	}
}

func TestKeystrokesAreIgnoredWhileAnActionIsInFlight(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}, busy: "opening session…"}

	after, _ := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if after.(uiModel).confirm != "" {
		t.Fatal("a keystroke started a second action while one was in flight")
	}
	// but ctrl+c must always work
	after2, cmd := u.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !after2.(uiModel).quitting || cmd == nil {
		t.Fatal("ctrl+c must not be swallowed while busy")
	}
}

func TestDstCWDFallsBackWhenTheSessionDirectoryIsGone(t *testing.T) {
	live := t.TempDir()
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	root := t.TempDir()

	u := uiModel{repoRoot: root}

	if got := u.dstCWD(&tree.Node{SessionCWD: live}); got != live {
		t.Fatalf("an existing session directory must be used: got %q want %q", got, live)
	}
	if got := u.dstCWD(&tree.Node{SessionCWD: gone}); got != root {
		t.Fatalf("a removed worktree must fall back to the repo root: got %q want %q", got, root)
	}
	if got := u.dstCWD(&tree.Node{SessionCWD: ""}); got != root {
		t.Fatalf("an empty session cwd must fall back to the repo root: got %q want %q", got, root)
	}
}

func TestOnlyTheLastTurnOfTheCurrentSessionIsMarkedCurrent(t *testing.T) {
	sess := adapter.Session{ID: "sid-a", Title: "t"}
	for _, id := range []string{"n1", "n2", "n3", "n4"} {
		sess.Nodes = append(sess.Nodes, adapter.Node{ID: id, Title: "turn " + id})
	}
	roots := tree.Build([]adapter.Session{sess}, &store.Store{Branches: map[string]store.Branch{}})
	rows := New(roots).Rows()

	var marked []string
	for _, r := range rows {
		if strings.Contains(renderRow(r, false, "sid-a", 80), "● current") {
			marked = append(marked, r.Node.Node.ID)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("want exactly one marked row for a 4-turn current session, got %v", marked)
	}
	if marked[0] != "n4" {
		t.Fatalf("marked row = %q want the last turn n4", marked[0])
	}
}

func TestScopeToggleKeepsCursorOnTheSameNode(t *testing.T) {
	s1 := chain("n1", "n2")
	s2 := &tree.Node{Node: adapter.Node{ID: "o1", Title: "turn o1"}, SessionID: "s2", IsSessionRoot: true}
	roots := []*tree.Node{s1, s2}
	u := uiModel{m: New(roots), current: "s1", roots: roots}
	u.rebuild() // starts scoped to s1
	if u.m.Selected() != s1 {
		t.Fatalf("setup: want cursor on s1's root, got %+v", u.m.Selected())
	}

	after, _ := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got := after.(uiModel)
	if !got.scopeAll {
		t.Fatal("want scopeAll true after toggling to all sessions")
	}
	if got.m.Selected() != s1 {
		t.Fatalf("toggling scope moved the cursor off a node that is still visible: %+v", got.m.Selected())
	}

	after2, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got2 := after2.(uiModel)
	if got2.scopeAll {
		t.Fatal("want scopeAll false after toggling back")
	}
	if got2.m.Selected() != s1 {
		t.Fatalf("toggling scope back moved the cursor: %+v", got2.m.Selected())
	}
}

func TestConfirmTextNamesWhatIsCarried(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "u3", Title: "what do you think"}}
	got := confirmText(n, 3, 12, 41984, "/home/somliga/projects/surtr")
	for _, want := range []string{"what do you think", "3 turn(s)", "12 entries", "41 KB", "/home/somliga/projects/surtr"} {
		if !strings.Contains(got, want) {
			t.Fatalf("confirm text missing %q:\n%s", want, got)
		}
	}
}

// Enter is the single "continue from here" key: on the session's tip it
// resumes with nothing written, and on any earlier turn it asks first
// because that path writes a graft.

func TestEnterOnSessionLeafResumesWithoutConfirming(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a", IsSessionLeaf: true}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}}

	after, cmd := u.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := after.(uiModel)
	if got.confirm != "" {
		t.Fatalf("resuming the tip must not confirm: %q", got.confirm)
	}
	if cmd == nil {
		t.Fatal("want a resume command")
	}
	msg, ok := cmd().(actionDoneMsg)
	if !ok {
		t.Fatalf("want actionDoneMsg from resumeCmd, got %T", cmd())
	}
	if !msg.quit {
		t.Fatal("want the resume to succeed and close the overlay")
	}
}

func TestEnterOnEarlierTurnConfirmsAndWritesNothingUntilConfirmed(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "an earlier turn"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}}

	after, cmd := u.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := after.(uiModel)
	if cmd != nil {
		t.Fatal("continuing from an earlier turn must not act before confirmation")
	}
	if got.confirm == "" {
		t.Fatal("want a confirmation before branching from an earlier turn")
	}
	if !strings.Contains(got.confirm, "an earlier turn") {
		t.Fatalf("confirmation does not name the turn: %q", got.confirm)
	}

	// Only the confirm dialog's own enter performs the branch.
	after2, cmd2 := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("want branchCmd once confirmed")
	}
	if after2.(uiModel).confirm != "" {
		t.Fatal("confirmation should close once acted on")
	}
}

func TestEnterOnBrokenRowDoesNothing(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a", Broken: true}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}}

	after, cmd := u.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := after.(uiModel)
	if cmd != nil {
		t.Fatal("a broken row must not resume")
	}
	if got.confirm != "" {
		t.Fatal("a broken row must not confirm a branch")
	}
}

func TestBKeyNoLongerBranches(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}}

	after, cmd := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if cmd != nil {
		t.Fatal("b must no longer trigger any action")
	}
	if after.(uiModel).confirm != "" {
		t.Fatal("b must no longer open the confirmation")
	}
}
