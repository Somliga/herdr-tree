package tui

import (
	"errors"
	"fmt"
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
	got, _ := renderRow(Row{Node: n, Depth: 1}, false, "", 80)
	if !strings.Contains(got, "i want to discuss the weather") {
		t.Fatalf("title missing: %q", got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("depth not indented: %q", got)
	}
}

func TestRenderRowMarksCurrent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a", IsSessionLeaf: true}
	got, _ := renderRow(Row{Node: n}, false, "sid-a", 80)
	if !strings.Contains(got, "● current") {
		t.Fatalf("current marker missing: %q", got)
	}
}

func TestRenderRowShowsSessionIdOnRoots(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "n1", Title: "x"},
		SessionID: "82cb69f2-e18b-4f86-874a-89e93139324a", IsSessionRoot: true,
	}
	got, _ := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "82cb69f2") {
		t.Fatalf("short session id missing: %q", got)
	}
	if strings.Contains(got, "e18b") {
		t.Fatalf("full uuid should not be shown: %q", got)
	}
}

func TestRenderRowMarksBroken(t *testing.T) {
	n := &tree.Node{SessionID: "sid", IsSessionRoot: true, Broken: true}
	got, _ := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("broken marker missing: %q", got)
	}
}

func TestRenderRowMarksGraft(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "m1", Title: "alt"},
		SessionID: "f2af34a4-x", IsSessionRoot: true, Grafted: true,
	}
	got, _ := renderRow(Row{Node: n, Depth: 2}, false, "", 80)
	if !strings.Contains(got, "↳") {
		t.Fatalf("graft marker missing: %q", got)
	}
}

// fakeAdapter lets the update loop be tested without Herdr or Claude. Its
// methods record what they were asked to do: a graft and a summary are both
// real work with real cost, so the assertions that matter are about which of
// them ran, with what.
type fakeAdapter struct {
	resumeErr    error
	summary      string
	summariseErr error
	seedErr      error

	summarisedFrom, summarisedTo string
	summarisedCompact            bool
	seededWith                   string
	branchedAt                   string // the node id Branch/BranchSeeded was called with
	resumed                      string
	focused                      bool

	span        adapter.Span
	spliceErr   error
	spliceErrAt int // fail only this splice, counting from 1
	spliced     []adapter.Edit
	splices     int
	writes      []string // "summarise <src>", "splice <src>" and "graft <src>", in order, failed ones too

	sessions    []adapter.Session // what Discover finds on a reload
	discoverErr error
}

func (f *fakeAdapter) Name() string                               { return "fake" }
func (f *fakeAdapter) Discover(string) ([]adapter.Session, error) {
	return f.sessions, f.discoverErr
}
func (f *fakeAdapter) Current(adapter.Pane) (string, error)       { return "", nil }
func (f *fakeAdapter) Preview(adapter.Session, string) (int, int, int64, error) {
	return 1, 2, 3, nil
}
func (f *fakeAdapter) Branch(_ adapter.Session, atNode, _ string) (string, error) {
	f.branchedAt = atNode
	return "new-sid", nil
}
func (f *fakeAdapter) BranchSeeded(src adapter.Session, atNode, _, seed string) (string, error) {
	f.writes = append(f.writes, "graft "+src.ID)
	f.branchedAt = atNode
	if f.seedErr != nil {
		return "", f.seedErr
	}
	f.seededWith = seed
	return "new-sid", nil
}
func (f *fakeAdapter) Resume(sessionID, _ string, focus bool) error {
	f.resumed, f.focused = sessionID, focus
	return f.resumeErr
}
func (f *fakeAdapter) Summarise(src adapter.Session, fromTurn, toTurn string, compact bool) (string, error) {
	f.writes = append(f.writes, "summarise "+src.ID)
	f.summarisedFrom, f.summarisedTo = fromTurn, toTurn
	f.summarisedCompact = compact
	if f.summariseErr != nil {
		return "", f.summariseErr
	}
	return f.summary, nil
}
func (f *fakeAdapter) Widen(adapter.Session, string, string) (adapter.Span, error) {
	return f.span, nil
}
func (f *fakeAdapter) Splice(src adapter.Session, e adapter.Edit, _ string) (adapter.Spliced, error) {
	f.writes = append(f.writes, "splice "+src.ID)
	f.splices++
	n := f.splices
	if f.spliceErr != nil {
		return adapter.Spliced{}, f.spliceErr
	}
	if n == f.spliceErrAt {
		return adapter.Spliced{}, errors.New("disk full")
	}
	f.spliced = append(f.spliced, e)
	sid := "spliced-sid"
	if n > 1 {
		sid = fmt.Sprintf("spliced%d-sid", n)
	}
	return adapter.Spliced{SessionID: sid, Removed: 2, After: "t3"}, nil
}

func TestFailedResumeKeepsTheOverlayOpen(t *testing.T) {
	// Bubble Tea discards its final frame when leaving the alt screen, so a
	// status set while quitting is never read. A failure must not quit.
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{resumeErr: errors.New("pane split refused")}}

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
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{}}

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
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{}, busy: "opening session…"}

	after, _ := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if after.(uiModel).confirm != "" {
		t.Fatal("a keystroke started a second action while one was in flight")
	}
	// ctrl+c must always get the user out — but not on the first press, which
	// only warns that the call is already billed. See
	// TestCtrlCDuringACallSaysTheSpendIsAlreadyCommitted.
	warned, cmd := u.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("the first ctrl+c must warn, not quit")
	}
	after2, cmd := warned.(uiModel).Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !after2.(uiModel).quitting || cmd == nil {
		t.Fatal("a second ctrl+c must not be swallowed while busy")
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
		text, _ := renderRow(r, false, "sid-a", 80)
		if strings.Contains(text, "● current") {
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

// familyFixture builds §5.3d's worked example: a trunk with two branches off
// the same turn (BITTEREND) — b and its sibling sib — plus an unrelated
// fourth session that must never appear once scoped. Titles are unique per
// row (the branch/sibling's copied BITTEREND is Superseded and gets no row
// of its own, so "turn BITTEREND" is unambiguous), which is what lets the
// tests below key off rendered text.
func familyFixture() ([]*tree.Node, *store.Store) {
	trunk := mkTypedSess("trunk",
		turn("hello", adapter.KindHuman),
		turn("BING", adapter.KindHuman),
		turn("BITTEREND", adapter.KindHuman),
		turn("FAN", adapter.KindHuman),
	)
	branch := mkTypedSess("b",
		turn("BITTEREND", adapter.KindHuman), // copied graft point, Superseded
		turn("reply", adapter.KindAssistant),
		turn("own2", adapter.KindHuman),
	)
	sibling := mkTypedSess("sib",
		turn("BITTEREND", adapter.KindHuman), // copied graft point, Superseded
		turn("other", adapter.KindHuman),
	)
	unrelated := mkTypedSess("unrelated", turn("elsewhere", adapter.KindHuman))

	st := &store.Store{Version: 1, Branches: map[string]store.Branch{
		"b":   {GraftedFrom: store.From{SessionID: "trunk", Node: "BITTEREND"}},
		"sib": {GraftedFrom: store.From{SessionID: "trunk", Node: "BITTEREND"}},
	}}
	return tree.Build([]adapter.Session{trunk, branch, sibling, unrelated}, st), st
}

// TestFamilyScopeShowsTheWholeFamily is spec test 1: from a branch session as
// current, the default scope's rows include the parent's rows and a sibling
// branch — but not an unrelated session.
func TestFamilyScopeShowsTheWholeFamily(t *testing.T) {
	roots, st := familyFixture()
	u := uiModel{m: New(roots), st: st, roots: roots, current: "b", width: 80, height: 40}
	u.rebuild()

	got := map[string]bool{}
	for _, r := range u.m.Rows() {
		got[r.Node.SessionID] = true
	}
	for _, want := range []string{"trunk", "b", "sib"} {
		if !got[want] {
			t.Errorf("family scope (current=b) is missing session %q: %v", want, got)
		}
	}
	if got["unrelated"] {
		t.Error("an unrelated session leaked into the family scope")
	}
}

// TestTrunkBarMarksExactlyThePathThroughTheFamily is spec test 2: the bar is
// on the parent's rows up to and including the branch point, then the
// branch's own rows — not the parent's tail after the branch point, and not
// the sibling.
func TestTrunkBarMarksExactlyThePathThroughTheFamily(t *testing.T) {
	roots, st := familyFixture()
	u := uiModel{m: New(roots), st: st, roots: roots, current: "b", width: 80, height: 40}
	u.rebuild()
	unfold(u)

	rows := u.m.Rows()
	lines := strings.Split(u.View(), "\n")
	byTitle := map[string]int{}
	for i, r := range rows {
		byTitle[r.Node.Node.Title] = i
	}
	onBar := func(title string) bool {
		i, ok := byTitle["turn "+title]
		if !ok {
			t.Fatalf("no row titled %q", "turn "+title)
		}
		return strings.Contains(lines[i], "▎")
	}
	for _, want := range []string{"hello", "BING", "BITTEREND", "reply", "own2"} {
		if !onBar(want) {
			t.Errorf("%q should be on b's path through the family (barred): %q", want, lines[byTitle["turn "+want]])
		}
	}
	for _, want := range []string{"FAN", "other"} {
		if onBar(want) {
			t.Errorf("%q should NOT be barred (parent's tail / sibling): %q", want, lines[byTitle["turn "+want]])
		}
	}
}

// TestNoCurrentSessionMeansNoTrunkBar is spec test 4: with no current
// session there is no bar on any row.
func TestNoCurrentSessionMeansNoTrunkBar(t *testing.T) {
	roots, st := familyFixture()
	u := uiModel{m: New(roots), st: st, roots: roots, current: "", width: 80, height: 40}
	u.rebuild()
	unfold(u)

	if strings.Contains(u.View(), "▎") {
		t.Fatalf("no current session, but a bar was drawn:\n%s", u.View())
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
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{}}

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
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{}}

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

// TestEnterOnAPromptRowGraftsAfterTheWholeTurn is §2.5b: a folded head row's
// own entry is the prompt, and grafting there would leave it unanswered for
// the resumed agent to answer again. The graft must land on the turn's last
// entry instead — what Widen(n, n) reports as its Span.End.
func TestEnterOnAPromptRowGraftsAfterTheWholeTurn(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "prompt-id", Title: "a prompt with a reply"}, SessionID: "sid-a"}
	fa := &fakeAdapter{span: adapter.Span{End: "reply-id"}}
	u := uiModel{m: New([]*tree.Node{n}), a: fa, st: loadedStore(t)}

	after, _ := u.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd2 := after.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("want branchCmd once confirmed")
	}
	cmd2()

	if fa.branchedAt != "reply-id" {
		t.Fatalf("want the graft at the turn's last entry %q, got %q", "reply-id", fa.branchedAt)
	}
}

func TestEnterOnBrokenRowDoesNothing(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a", Broken: true}
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{}}

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
	u := uiModel{m: New([]*tree.Node{n}), a: &fakeAdapter{}}

	after, cmd := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if cmd != nil {
		t.Fatal("b must no longer trigger any action")
	}
	if after.(uiModel).confirm != "" {
		t.Fatal("b must no longer open the confirmation")
	}
}

// Toggling scope mid-selection used to discard the range silently, with the
// very same nodes still on screen: tree.Build runs once, in Run, so a scope
// toggle re-roots the SAME pointers. No test drove the real rebuild() path
// with a range active — the one that claimed to build two independent trees
// and cross-assigned their nodes, which rebuild() cannot produce.
func TestScopeToggleKeepsAnActiveRange(t *testing.T) {
	roots := tree.Build([]adapter.Session{
		{ID: "s1", Nodes: []adapter.Node{{ID: "t1", Title: "one"}, {ID: "t2", Title: "two"}, {ID: "t3", Title: "three"}}},
	}, &store.Store{Version: 1, Branches: map[string]store.Branch{}})

	u := uiModel{m: New(roots), roots: roots, current: "s1", scopeAll: true}
	for u.m.Rows()[0].Folded {
		u.m.Unfold()
	}
	u.m.Cursor = 1
	u.m.BeginRange()
	end := u.m.RangeEnd

	u.scopeAll = false
	u.rebuild()

	if u.m.RangeEnd != end {
		t.Fatalf("a scope toggle discarded the in-progress range: %v", u.m.RangeEnd)
	}
}

// loadedStore is a real store on a temporary path. A hand-built Store has no
// path, so Save fails — and foldBackCmd correctly stops before opening the
// session when the edge could not be recorded, which makes every success path
// untestable against a fixture that cannot occur in reality.
func loadedStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	st, err := store.Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// session builds the forest the real code builds: tree.Build is the only
// thing that makes tree.Nodes in production, and it is what sets IsHead,
// IsSessionRoot and IsSessionLeaf. Hand-built chains have got those wrong
// before.
func session(id string, turns ...string) []*tree.Node {
	sess := adapter.Session{ID: id, Title: id, CWD: "/repo", Path: "/transcripts/" + id + ".jsonl"}
	for _, t := range turns {
		sess.Nodes = append(sess.Nodes, adapter.Node{ID: t, Title: "turn " + t, Kind: adapter.KindHuman})
	}
	return tree.Build([]adapter.Session{sess}, &store.Store{Version: 1, Branches: map[string]store.Branch{}})
}

func TestSummariseStoresTheRangeAndKeepsTheOverlayOpen(t *testing.T) {
	st := loadedStore(t)
	roots := session("s", "t1", "t2", "t3")
	m := New(roots)
	from, to := m.Rows()[0].Node, m.Rows()[2].Node

	fa := &fakeAdapter{summary: "it went well"}
	op := editOp{src: adapter.Session{ID: "s"}, kind: store.KindCompacted, summarise: true, from: from, to: to}
	msg := editCmd(fa, st, op, nil)().(actionDoneMsg)

	if msg.quit {
		t.Fatal("summarising must not close the overlay: nothing has been opened")
	}
	if !msg.reload {
		t.Fatal("a not-live edit reloads the tree")
	}
	if fa.summarisedFrom != "t1" || fa.summarisedTo != "t3" {
		t.Fatalf("summarised %q..%q, want t1..t3", fa.summarisedFrom, fa.summarisedTo)
	}
	got := summariesFor(st, "s")
	if len(got) != 1 {
		t.Fatalf("got %d summaries for s, want 1", len(got))
	}
	if got[0].Text != "it went well" {
		t.Fatal("the stored summary is not the model's text")
	}
	if got[0].FromTurn != "t1" || got[0].ToTurn != "t3" {
		t.Fatalf("stored span %q..%q, want t1..t3", got[0].FromTurn, got[0].ToTurn)
	}
}

func TestFailedSummariseStoresNothingAndKeepsTheOverlayOpen(t *testing.T) {
	st := loadedStore(t)
	roots := session("s", "t1", "t2")
	m := New(roots)
	from, to := m.Rows()[0].Node, m.Rows()[1].Node

	fa := &fakeAdapter{summariseErr: errors.New("claude: credit balance too low")}
	op := editOp{src: adapter.Session{ID: "s"}, kind: store.KindCompacted, summarise: true, from: from, to: to}
	msg := editCmd(fa, st, op, nil)().(actionDoneMsg)

	if msg.quit {
		t.Fatal("a failed summarise must not quit: the message would never be seen")
	}
	if !strings.Contains(msg.status, "credit balance too low") {
		t.Fatalf("status does not carry the cause: %q", msg.status)
	}
	if len(summariesFor(st, "s")) != 0 {
		t.Fatal("a failed summarise recorded a summary")
	}
}

func TestFoldBackAtAnEarlierTurnSeedsAGraft(t *testing.T) {
	fa := &fakeAdapter{}
	st := loadedStore(t)
	at := New(session("s", "t1", "t2")).Rows()[0].Node // an earlier turn, not the tip
	sum := store.Summary{Text: "what the branch found", SessionID: "other", FromTurn: "a", ToTurn: "b"}

	msg := foldBackCmd(fa, st, at, at.Node.ID, "/repo", sum, "", nil)().(actionDoneMsg)

	if fa.seededWith == "" {
		t.Fatal("an earlier turn must be seeded via BranchSeeded")
	}
	if !strings.HasPrefix(fa.seededWith, claudeSummaryPrefix) {
		t.Fatal("the seed is not marked as a summary; GraftSeeded would refuse it")
	}
	if !strings.Contains(fa.seededWith, sum.Text) {
		t.Fatal("the seed lost the summary text")
	}
	if fa.resumed != "" {
		t.Fatalf("a fold-back opens nothing (spec §2.5): resumed %q", fa.resumed)
	}
	if !msg.reload || msg.quit {
		t.Fatalf("a successful fold-back reloads the tree and stays open: %+v", msg)
	}
	if _, ok := st.Branches["new-sid"]; !ok {
		t.Fatalf("no graft edge recorded: %+v", st.Branches)
	}
}

func TestFoldBackOfThisLinesOwnSummaryIsMarkedAsCompaction(t *testing.T) {
	// Compaction is fold-back with the range's own line as the destination.
	// Only the prefix distinguishes the two, and the classifier and the
	// palette read nothing else — so getting it wrong makes every compaction
	// render as an import.
	fa := &fakeAdapter{}
	st := loadedStore(t)
	at := New(session("s", "t1", "t2", "t3")).Rows()[0].Node
	sum := store.Summary{Text: "eight turns of auth work", SessionID: "s", FromTurn: "t2", ToTurn: "t3"}

	foldBackCmd(fa, st, at, at.Node.ID, "/repo", sum, "", nil)()

	if !strings.HasPrefix(fa.seededWith, claudeCompactionPrefix) {
		t.Fatal("a summary of this same session must seed as a compaction")
	}
}

func TestFoldBackAtTheLiveTipSendsAMessage(t *testing.T) {
	fa := &fakeAdapter{}
	st := loadedStore(t)
	rows := New(session("s", "t1", "t2")).Rows()
	tip := rows[len(rows)-1].Node
	if !tip.IsSessionLeaf {
		t.Fatal("setup: the last row of a session is its leaf")
	}
	sum := store.Summary{Text: "folded", SessionID: "other", FromTurn: "a", ToTurn: "b"}

	var gotAgent, gotText string
	send := func(agent, text string) error { gotAgent, gotText = agent, text; return nil }
	msg := foldBackCmd(fa, st, tip, tip.Node.ID, "/repo", sum, "tree-agent", send)().(actionDoneMsg)

	if fa.seededWith != "" {
		t.Fatal("the live tip must not be grafted: a 6.6MB copy to deliver one message")
	}
	if gotAgent != "tree-agent" {
		t.Fatalf("sent to agent %q, want tree-agent", gotAgent)
	}
	if !strings.HasPrefix(gotText, claudeSummaryPrefix) || !strings.Contains(gotText, sum.Text) {
		t.Fatal("the message is not the marked summary")
	}
	if !msg.quit {
		t.Fatal("a delivered message closes the overlay: the conversation is where to look next")
	}
	if len(st.Branches) != 0 {
		t.Fatalf("a message must not record a graft edge: %+v", st.Branches)
	}
}

func TestABlockedAgentSurfacesAndDoesNotGraftInstead(t *testing.T) {
	// The user asked to continue a conversation. Forking one instead gives
	// them two lines where they expected one, and they will not notice.
	fa := &fakeAdapter{}
	st := loadedStore(t)
	rows := New(session("s", "t1", "t2")).Rows()
	tip := rows[len(rows)-1].Node

	blocked := errors.New("agent is waiting for input of its own")
	send := func(string, string) error { return blocked }
	msg := foldBackCmd(fa, st, tip, tip.Node.ID, "/repo",
		store.Summary{Text: "x", SessionID: "other"}, "tree-agent", send)().(actionDoneMsg)

	if fa.seededWith != "" {
		t.Fatal("a blocked agent must not fall back to grafting")
	}
	if msg.quit {
		t.Fatal("a failed send must not quit: the message would never be seen")
	}
	if !strings.Contains(msg.status, blocked.Error()) {
		t.Fatalf("status does not say why: %q", msg.status)
	}
	if len(st.Branches) != 0 {
		t.Fatalf("nothing was written, so nothing may be recorded: %+v", st.Branches)
	}
}

// s fixes the range's END, the cursor then picks the START, and the cost is
// shown before a single API call is made.
func TestSKeyFixesTheRangeEndThenConfirmsBeforeSummarising(t *testing.T) {
	st := loadedStore(t)
	fa := &fakeAdapter{summary: "it went well", span: adapter.Span{First: 1, Last: 3}}
	roots := session("s", "t1", "t2", "t3")
	u := uiModel{m: New(roots), a: fa, st: st}
	u.m.Cursor = 2

	after, cmd := u.Update(key('s'))
	got := after.(uiModel)
	if cmd != nil {
		t.Fatal("fixing the range end must not act")
	}
	if got.m.RangeEnd != got.m.Rows()[2].Node {
		t.Fatalf("the range end is not the row s was pressed on: %+v", got.m.RangeEnd)
	}

	got.m.Cursor = 0
	after2, cmd2 := got.Update(key('s'))
	got2 := after2.(uiModel)
	if cmd2 != nil {
		t.Fatal("the range menu must not act on its own")
	}
	if got2.menu != "range" {
		t.Fatalf("want the range menu, got %q", got2.menu)
	}

	// enter picks "summarise & compact", the first option in the menu.
	after2b, cmd2b := got2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got2b := after2b.(uiModel)
	if cmd2b != nil {
		t.Fatal("summarising must not start before the cost is confirmed")
	}
	if fa.summarisedTo != "" {
		t.Fatal("the adapter was called before confirmation")
	}
	if !strings.Contains(got2b.confirm, "turns 1–3") {
		t.Fatalf("the confirmation does not say how much is being summarised:\n%s", got2b.confirm)
	}
	if !strings.Contains(got2b.confirm, "turn t1") || !strings.Contains(got2b.confirm, "turn t3") {
		t.Fatalf("the confirmation does not name both ends:\n%s", got2b.confirm)
	}

	after3, cmd3 := got2b.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got3 := after3.(uiModel)
	if cmd3 == nil {
		t.Fatal("want editCmd once confirmed")
	}
	if got3.busy == "" {
		t.Fatal("want the overlay to say an API call is in flight")
	}
	if got3.m.RangeEnd != nil {
		t.Fatal("acting on a range consumes it")
	}
	if msg := cmd3().(actionDoneMsg); msg.quit {
		t.Fatal("summarising must not close the overlay")
	}
	if fa.summarisedFrom != "t1" || fa.summarisedTo != "t3" {
		t.Fatalf("summarised %q..%q, want t1..t3 in document order", fa.summarisedFrom, fa.summarisedTo)
	}
	if len(fa.spliced) != 1 {
		t.Fatalf("want one splice, got %+v", fa.spliced)
	}
}

func TestEscCancelsAnInProgressRangeInsteadOfClosing(t *testing.T) {
	u := uiModel{m: New(session("s", "t1", "t2")), a: &fakeAdapter{}}
	u.m.Cursor = 1
	u.m.BeginRange()

	after, cmd := u.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := after.(uiModel)
	if got.quitting || cmd != nil {
		t.Fatal("esc with a range in progress must not close the overlay")
	}
	if got.m.RangeEnd != nil {
		t.Fatal("esc must cancel the range")
	}

	// and with no range it still closes
	after2, cmd2 := got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !after2.(uiModel).quitting || cmd2 == nil {
		t.Fatal("esc with no range must still close the overlay")
	}
}

func TestPWithNoSummariesOffersToMakeOneRatherThanRefusing(t *testing.T) {
	st := loadedStore(t)
	u := uiModel{m: New(session("s", "t1", "t2")), a: &fakeAdapter{}, st: st}

	after, cmd := u.Update(key('p'))
	got := after.(uiModel)
	if cmd != nil || got.picking != nil {
		t.Fatal("there is nothing to pick")
	}
	if !strings.Contains(got.status, "s selects a range to squash") {
		t.Fatalf("the offer does not say how to get a summary: %q", got.status)
	}
}

func TestPickerFoldsTheChosenSummaryInAtTheSelectedTurn(t *testing.T) {
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "what the branch found", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	fa := &fakeAdapter{}
	u := uiModel{m: New(session("s", "t1", "t2")), a: fa, st: st, repoRoot: "/repo"}
	u.m.Cursor = 0 // an earlier turn

	after, _ := u.Update(key('p'))
	got := after.(uiModel)
	if len(got.picking) != 1 || got.pickAt != got.m.Rows()[0].Node {
		t.Fatalf("the picker did not open on the selected turn: %+v", got.pickAt)
	}
	if !strings.Contains(got.View(), "merge it here, or branch here") {
		t.Fatalf("the picker does not say what enter will do:\n%s", got.View())
	}

	after2, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got2 := after2.(uiModel)
	if cmd != nil {
		t.Fatal("the picker must not graft before the cost is shown")
	}
	if got2.picking != nil {
		t.Fatal("the picker should close once acted on")
	}
	after2b, _ := got2.Update(tea.KeyMsg{Type: tea.KeyDown})
	after2c, _ := after2b.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got2 = after2c.(uiModel)
	// The same graft ⏎ on a turn confirms, plus a pane open: the same
	// figures, from the same Preview.
	for _, want := range []string{"1 turn(s)", "2 entries", "3 B"} {
		if !strings.Contains(got2.confirm, want) {
			t.Fatalf("the fold-back confirmation does not say %q:\n%s", want, got2.confirm)
		}
	}
	if fa.seededWith != "" {
		t.Fatal("nothing may be written before the confirmation is accepted")
	}

	after3, cmd3 := got2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd3 == nil {
		t.Fatal("want foldBackCmd once confirmed")
	}
	if after3.(uiModel).confirm != "" {
		t.Fatal("the confirmation should close once acted on")
	}
	if msg := cmd3().(actionDoneMsg); !msg.reload {
		t.Fatalf("want the fold-back to succeed: %q", msg.status)
	}
	if !strings.HasPrefix(fa.seededWith, claudeSummaryPrefix) {
		t.Fatal("the chosen summary was not seeded into the graft")
	}
	if !strings.Contains(fa.seededWith, "what the branch found") {
		t.Fatal("the seed does not carry the summary the picker was on")
	}
}

// TestBranchHereGraftsAfterTheWholeTurn is §2.5b's second path: branch here
// (from p's place menu) must graft at the turn's last entry too, not at the
// row it was chosen on.
func TestBranchHereGraftsAfterTheWholeTurn(t *testing.T) {
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "what the branch found", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	fa := &fakeAdapter{span: adapter.Span{End: "reply-id"}}
	u := uiModel{m: New(session("s", "t1", "t2")), a: fa, st: st, repoRoot: "/repo"}
	u.m.Cursor = 0 // an earlier turn, not the tip

	after, _ := u.Update(key('p'))
	after2, _ := after.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})     // picks the summary
	after3, _ := after2.(uiModel).Update(tea.KeyMsg{Type: tea.KeyDown})     // place menu: branch here
	after4, _ := after3.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})    // shows the cost
	_, cmd := after4.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})       // confirms
	if cmd == nil {
		t.Fatal("want foldBackCmd once confirmed")
	}
	cmd()

	if fa.branchedAt != "reply-id" {
		t.Fatalf("want the graft at the turn's last entry %q, got %q", "reply-id", fa.branchedAt)
	}
}

// Only the tip of the session the user is actually in can take a message. A
// leaf of some other session is the end of a conversation nobody is holding,
// and sending there would deliver the summary into the wrong agent.
func TestOnlyTheLiveSessionsTipIsSentTo(t *testing.T) {
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "folded", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	fa := &fakeAdapter{}
	sent := 0
	u := uiModel{
		m: New(session("s", "t1", "t2")), a: fa, st: st, repoRoot: "/repo",
		current: "somebody-else", liveAgent: "tree-agent",
		send: func(string, string) error { sent++; return nil },
	}
	rows := u.m.Rows()
	u.m.Cursor = len(rows) - 1 // this session's tip, but not the live session

	after, _ := u.Update(key('p'))
	got := after.(uiModel)
	if strings.Contains(got.View(), "tree-agent") {
		t.Fatalf("the picker offers to message an agent that is not on this session:\n%s", got.View())
	}
	after2, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("the graft path must confirm first")
	}
	after2b, _ := after2.(uiModel).Update(tea.KeyMsg{Type: tea.KeyDown})
	after2c, _ := after2b.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd = after2c.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("want foldBackCmd once confirmed")
	}
	cmd()
	if sent != 0 {
		t.Fatal("a summary was sent to the agent of a different session")
	}
	if fa.seededWith == "" {
		t.Fatal("a tip nobody is holding must be grafted")
	}
}

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// A summary IS message content. It may appear on the screen the user asked
// for it on, and nowhere else: not in a status line, not in an error, and not
// in a title that goes to disk and comes back as a row.
const sentinel = "PRIVATE-CONVERSATION-CONTENT"

func summaryWithSentinel() store.Summary {
	return store.Summary{
		Text:      "attempted: the token service\n\nrejected: redis, because " + sentinel + " and more besides",
		SessionID: "other", FromTurn: "a", ToTurn: "b",
	}
}

func TestNoStatusLineCarriesTheSummary(t *testing.T) {
	sum := summaryWithSentinel()
	roots := session("s", "t1", "t2")
	rows := New(roots).Rows()
	early, tip := rows[0].Node, rows[len(rows)-1].Node

	var statuses []string
	collect := func(m tea.Msg) { statuses = append(statuses, m.(actionDoneMsg).status) }

	// summarising, succeeding
	st := loadedStore(t)
	op := editOp{src: adapter.Session{ID: "s"}, kind: store.KindCompacted, summarise: true, from: early, to: tip}
	collect(editCmd(&fakeAdapter{summary: sum.Text}, st, op, nil)())
	// folding back as a graft, succeeding
	collect(foldBackCmd(&fakeAdapter{}, loadedStore(t), early, early.Node.ID, "/repo", sum, "", nil)())
	// folding back as a message, succeeding
	collect(foldBackCmd(&fakeAdapter{}, loadedStore(t), tip, tip.Node.ID, "/repo", sum, "wA:p1",
		func(string, string) error { return nil })())
	// and failing with an error that quotes the message back at us, which is
	// exactly what herdr does with an argument it would not accept
	quoting := func(_, text string) error { return errors.New("herdr agent prompt: rejected " + text) }
	collect(foldBackCmd(&fakeAdapter{}, loadedStore(t), tip, tip.Node.ID, "/repo", sum, "wA:p1", quoting)())
	// and the graft path failing the same way
	echoing := &fakeAdapter{seedErr: errors.New("graft refused: " + sum.Text)}
	collect(foldBackCmd(echoing, loadedStore(t), early, early.Node.ID, "/repo", sum, "", nil)())

	if len(statuses) != 5 {
		t.Fatalf("setup: collected %d statuses", len(statuses))
	}
	for i, got := range statuses {
		if strings.Contains(got, sentinel) {
			t.Fatalf("status %d leaked the summary: %q", i, got)
		}
	}
	// the failures must still say something happened
	for _, i := range []int{3, 4} {
		if statuses[i] == "" {
			t.Fatalf("status %d says nothing about a failure", i)
		}
	}
}

func TestAStoredTitleIsABoundedSingleLine(t *testing.T) {
	st := loadedStore(t)
	at := New(session("s", "t1", "t2")).Rows()[0].Node
	foldBackCmd(&fakeAdapter{}, st, at, at.Node.ID, "/repo", summaryWithSentinel(), "", nil)()

	b, ok := st.Branches["new-sid"]
	if !ok {
		t.Fatalf("no edge recorded: %+v", st.Branches)
	}
	if strings.Contains(b.Title, sentinel) {
		t.Fatalf("the stored title carries the summary's body: %q", b.Title)
	}
	if strings.ContainsAny(b.Title, "\n\r") {
		t.Fatalf("a row title must be one line: %q", b.Title)
	}
	if n := len([]rune(b.Title)); n > 44 {
		t.Fatalf("a row title must be bounded: %d runes", n)
	}
}

// A range is selected by row, and with scope set to all sessions the rows of
// two sessions sit next to each other. Summarise grafts ONE transcript and
// names the start turn in it, so a start in another session is not in that
// file at all.
func TestARangeMayNotSpanTwoSessions(t *testing.T) {
	roots := tree.Build([]adapter.Session{
		{ID: "s1", Nodes: []adapter.Node{{ID: "t1", Title: "one"}, {ID: "t2", Title: "two"}}},
		{ID: "s2", Nodes: []adapter.Node{{ID: "u1", Title: "other one"}}},
	}, &store.Store{Version: 1, Branches: map[string]store.Branch{}})

	fa := &fakeAdapter{summary: "x"}
	u := uiModel{m: New(roots), a: fa, st: loadedStore(t), roots: roots, scopeAll: true}
	rows := u.m.Rows()
	if len(rows) != 3 || rows[0].Node.SessionID == rows[2].Node.SessionID {
		t.Fatalf("setup: want rows from two sessions, got %d", len(rows))
	}
	u.m.Cursor = 2 // the other session's turn
	u.m.BeginRange()
	u.m.Cursor = 0

	after, cmd := u.Update(key('s'))
	got := after.(uiModel)
	if cmd != nil || got.confirm != "" {
		t.Fatal("a range across two sessions must not be summarised")
	}
	if fa.summarisedTo != "" {
		t.Fatal("the adapter was asked to summarise across two transcripts")
	}
	if !strings.Contains(got.status, "one session") {
		t.Fatalf("the refusal does not say why: %q", got.status)
	}
	if got.m.RangeEnd == nil {
		t.Fatal("the range should survive so it can be adjusted")
	}
}

func TestPOnABrokenRowDoesNothing(t *testing.T) {
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "x", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	// tree.Build's own shape for a session whose transcript will not parse:
	// one node, no turn id, Broken.
	roots := tree.Build([]adapter.Session{{ID: "s", Broken: true}}, st)
	u := uiModel{m: New(roots), a: &fakeAdapter{}, st: st}

	after, cmd := u.Update(key('p'))
	got := after.(uiModel)
	if cmd != nil || got.picking != nil {
		t.Fatal("a broken row has no turn to fold a summary into")
	}
}

// The picker's sentence is a promise about what enter will do. A mid-session
// turn of the live session is still a graft — the message path only exists at
// the tip — and saying otherwise is how a summary ends up somewhere the user
// did not put it.
func TestThePickerOnlyPromisesToSendAtTheTip(t *testing.T) {
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "folded", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	sent := 0
	u := uiModel{
		m: New(session("s", "t1", "t2")), a: &fakeAdapter{}, st: st, repoRoot: "/repo",
		current: "s", liveAgent: "wA:p1",
		send: func(string, string) error { sent++; return nil },
	}
	u.m.Cursor = 0 // the live session, but not its tip

	after, _ := u.Update(key('p'))
	got := after.(uiModel)
	if strings.Contains(got.View(), "wA:p1") {
		t.Fatalf("the picker promises to send at a mid-session turn:\n%s", got.View())
	}

	after2, _ := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after2b, _ := after2.(uiModel).Update(tea.KeyMsg{Type: tea.KeyDown})
	after2c, _ := after2b.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd := after2c.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("want foldBackCmd once confirmed")
	}
	cmd()
	if sent != 0 {
		t.Fatal("a mid-session turn was sent to the live agent")
	}

	// and at the tip it does promise, and does send
	u.m.Cursor = len(u.m.Rows()) - 1
	after3, _ := u.Update(key('p'))
	got3 := after3.(uiModel)
	if !strings.Contains(got3.View(), "wA:p1") {
		t.Fatalf("the picker does not say it will send at the live tip:\n%s", got3.View())
	}
	_, cmd3 := got3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd3 == nil {
		t.Fatal("the send path acts on enter: there is no cost to confirm")
	}
	cmd3()
	if sent != 1 {
		t.Fatalf("the live tip did not send: %d", sent)
	}
}

// Escaping the cost dialog is how you go back and move the range's start.
// Losing the range there would mean re-selecting it to find out what the
// cheaper version costs.
func TestEscapingTheCostDialogKeepsTheRange(t *testing.T) {
	roots := session("s", "t1", "t2", "t3")
	fa := &fakeAdapter{summary: "x"}
	u := uiModel{m: New(roots), a: fa, st: loadedStore(t)}
	u.m.Cursor = 2
	u.m.BeginRange()
	u.m.Cursor = 0

	afterMenu, _ := u.Update(key('s'))
	// enter picks "summarise & compact", the first option in the menu.
	after, _ := afterMenu.(uiModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := after.(uiModel)
	if got.confirm == "" {
		t.Fatal("setup: want the cost dialog")
	}

	after2, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got2 := after2.(uiModel)
	if cmd != nil || got2.quitting {
		t.Fatal("esc on the dialog dismisses it, nothing else")
	}
	if got2.confirm != "" {
		t.Fatal("the dialog should close")
	}
	if got2.m.RangeEnd != got2.m.Rows()[2].Node {
		t.Fatalf("the range was discarded: %+v", got2.m.RangeEnd)
	}
	if fa.summarisedTo != "" {
		t.Fatal("nothing may have been summarised")
	}
}

func TestSOnAnEmptyTreeClaimsNothing(t *testing.T) {
	u := uiModel{m: New(nil), a: &fakeAdapter{}, st: loadedStore(t)}
	after, cmd := u.Update(key('s'))
	got := after.(uiModel)
	if cmd != nil || got.m.RangeEnd != nil {
		t.Fatal("there is no row to fix a range end on")
	}
	if got.status != "" {
		t.Fatalf("the status claims a range was started: %q", got.status)
	}
}

// Quitting mid-call exits the process, and the watchdog that would kill the
// model call dies with it: the call completes and is billed whether the user
// waits or not. So the first ctrl+c must not leave silently — it has to say
// the money is already spent, and only a second press abandons it.
func TestCtrlCDuringACallSaysTheSpendIsAlreadyCommitted(t *testing.T) {
	u := uiModel{m: New([]*tree.Node{chain("t1")}), busy: "summarising…", width: 80, height: 24}

	m1, cmd := u.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	first := m1.(uiModel)
	if cmd != nil {
		t.Fatal("the first ctrl+c must not quit; the call is billed either way")
	}
	if !strings.Contains(first.View(), "already billed") {
		t.Fatalf("the first ctrl+c said nothing about the spend:\n%s", first.View())
	}

	m2, cmd := first.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("a second ctrl+c must still let the user out")
	}
	if !m2.(uiModel).quitting {
		t.Fatal("the second ctrl+c must quit")
	}
}

// Folding a summary of this session's own turns onto its LIVE tip replaces
// nothing — every turn is still ahead of it — so calling it a compaction
// paints blue ("this line contracted") over a line that did not. Only the
// graft path rewinds.
func TestOnlyARewindIsCalledACompaction(t *testing.T) {
	at := &tree.Node{Node: adapter.Node{ID: "t20"}, SessionID: "s1", IsSessionLeaf: true}
	sum := store.Summary{SessionID: "s1", FromTurn: "t5", ToTurn: "t12", Text: "what we settled"}

	if got := foldBackSeed(at, sum, true); !strings.HasPrefix(got, claudeCompactionPrefix) {
		t.Fatalf("a rewind of this line's own turns is a compaction, got %q", firstLineOf(got))
	}
	sent := foldBackSeed(at, sum, false)
	if strings.HasPrefix(sent, claudeCompactionPrefix) {
		t.Fatalf("appending to a live tip contracts nothing, so it is not a compaction: %q", firstLineOf(sent))
	}
	if !strings.HasPrefix(sent, claudeSummaryPrefix) {
		t.Fatalf("an injected entry must still carry a marker: %q", firstLineOf(sent))
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// summariesFor is what the store no longer provides: nothing in the plugin
// asked for one session's summaries, so the accessor went.
func summariesFor(st *store.Store, sessionID string) []store.Summary {
	var out []store.Summary
	for _, v := range st.AllSummaries() {
		if v.SessionID == sessionID {
			out = append(out, v)
		}
	}
	return out
}

func TestRenderRowShowsCutsAndRemovedOrigins(t *testing.T) {
	// The cut marker is drawn apart from the row (in View, via cutNote) so it
	// can be muted whatever the row's own style — renderRow's line must not
	// carry it at all.
	here := &tree.Node{Node: adapter.Node{ID: "t4", Title: "four", Kind: adapter.KindHuman}, SessionID: "s", CutHere: 8}
	if got, _ := renderRow(Row{Node: here}, false, "", 120); strings.Contains(got, "✂") {
		t.Fatalf("cut marker must not be in the row's own line: %q", got)
	}
	if got := cutNote(here); got != "   ✂ 8 turns dropped before this" {
		t.Fatalf("cutNote(CutHere) = %q", got)
	}
	end := &tree.Node{Node: adapter.Node{ID: "t2", Title: "two", Kind: adapter.KindHuman}, SessionID: "s", IsSessionLeaf: true, CutAfter: 3}
	if got, _ := renderRow(Row{Node: end}, false, "", 120); strings.Contains(got, "✂") {
		t.Fatalf("trailing cut marker must not be in the row's own line: %q", got)
	}
	if got := cutNote(end); got != "   ✂ 3 turns dropped after this" {
		t.Fatalf("cutNote(CutAfter) = %q", got)
	}
	orphan := &tree.Node{Node: adapter.Node{ID: "b1", Title: "b"}, SessionID: "br", IsSessionRoot: true, FromRemoved: true}
	if got, _ := renderRow(Row{Node: orphan}, false, "", 120); !strings.Contains(got, "from a removed stretch") {
		t.Fatalf("removed-origin marker missing: %q", got)
	}
}

// TestViewMutesTheCutMarker checks the muting itself: the cut marker in the
// full View output must carry StyleTool's rendering, whatever style the row
// it sits on takes (spec §5.4) — renderRow no longer even sees it.
func TestViewMutesTheCutMarker(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut, Cut: &store.Cut{Turns: 2, At: "t2"}})
	roots := tree.Build([]adapter.Session{
		{ID: "new", Nodes: []adapter.Node{{ID: "t1", Title: "one"}, {ID: "t2", Title: "two"}}},
	}, st)
	u := uiModel{m: New(roots), roots: roots, current: "new"}
	for u.m.Rows()[0].Folded {
		u.m.Unfold()
	}
	var cut *tree.Node
	for _, r := range u.m.Rows() {
		if r.Node.CutHere > 0 {
			cut = r.Node
		}
	}
	if cut == nil {
		t.Fatal("no row carries the cut")
	}
	want := render(StyleTool, cutNote(cut))
	if got := u.View(); !strings.Contains(got, want) {
		t.Fatalf("View output does not mute the cut marker as StyleTool:\ngot:  %q\nwant substring: %q", got, want)
	}
}
