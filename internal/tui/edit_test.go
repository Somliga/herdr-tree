package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

// herdrLog records what the handover asked herdr for, in order.
type herdrLog struct {
	status   []string // successive answers to live(); the last repeats
	liveErr  error
	closeErr error
	calls    []string
}

func (h *herdrLog) live(sid string) (string, string, error) {
	h.calls = append(h.calls, "live "+sid)
	if h.liveErr != nil {
		return "", "", h.liveErr
	}
	if len(h.status) == 0 {
		return "", "", nil
	}
	s := h.status[0]
	if len(h.status) > 1 {
		h.status = h.status[1:]
	}
	return "pane-1", s, nil
}

func (h *herdrLog) close(p string) error {
	h.calls = append(h.calls, "close "+p)
	return h.closeErr
}

func rangeUI(t *testing.T, fa *fakeAdapter, h *herdrLog) uiModel {
	t.Helper()
	u := uiModel{m: New(session("s", "t1", "t2", "t3")), a: fa, st: loadedStore(t), repoRoot: "/repo",
		live: h.live, closePane: h.close}
	u.m.Cursor = 2
	next, _ := u.Update(key('s'))
	u = next.(uiModel)
	u.m.Cursor = 1
	return u
}

func press(t *testing.T, u uiModel, msgs ...tea.KeyMsg) (uiModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, m := range msgs {
		var next tea.Model
		next, cmd = u.Update(m)
		u = next.(uiModel)
	}
	return u, cmd
}

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	down  = tea.KeyMsg{Type: tea.KeyDown}
	esc   = tea.KeyMsg{Type: tea.KeyEsc}
)

func TestARangeOpensAMenuNotACall(t *testing.T) {
	fa := &fakeAdapter{}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{}), enter)
	if cmd != nil || u.menu != "range" {
		t.Fatalf("want the range menu and no command; menu=%q", u.menu)
	}
	for _, want := range []string{"summarise & compact", "cut"} {
		if !strings.Contains(u.View(), want) {
			t.Fatalf("menu does not offer %q:\n%s", want, u.View())
		}
	}
	u, _ = press(t, u, esc)
	if u.menu != "" || u.m.RangeEnd == nil {
		t.Fatal("esc on the menu must close it and keep the range")
	}
}

func TestCutConfirmsOnceThenSplicesAndReplaces(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{}), enter, down, enter)
	if cmd != nil {
		t.Fatal("cut ran before its confirmation")
	}
	for _, want := range []string{"turns 2–3", "Costs nothing", "hidden"} {
		if !strings.Contains(u.confirm, want) {
			t.Fatalf("cut confirmation lacks %q:\n%s", want, u.confirm)
		}
	}
	u, cmd = press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 1 || fa.spliced[0].Seed != "" {
		t.Fatalf("want one cut, got %+v", fa.spliced)
	}
	if fa.resumed != "" {
		t.Fatal("a session no pane holds must not be opened")
	}
	if u.st.Branches["s"].ReplacedBy != "spliced-sid" {
		t.Fatal("the old line was not marked replaced")
	}
	nb := u.st.Branches["spliced-sid"]
	if nb.Kind != store.KindCut || nb.Cut == nil || nb.Cut.Turns != 2 || nb.Cut.At != "t3" {
		t.Fatalf("new record %+v", nb)
	}
	if !msg.reload || msg.quit {
		t.Fatalf("a not-live edit reloads the tree and stays open: %+v", msg)
	}
}

func TestCompactSummarisesThenSplicesWithTheSeed(t *testing.T) {
	fa := &fakeAdapter{summary: "it went well", span: adapter.Span{First: 2, Last: 3}}
	u, _ := press(t, rangeUI(t, fa, &herdrLog{}), enter, enter)
	if !strings.Contains(u.confirm, "billed") || !strings.Contains(u.confirm, "replaced by the summary") {
		t.Fatalf("compact confirmation:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	cmd()
	if fa.summarisedFrom != "t2" || fa.summarisedTo != "t3" {
		t.Fatalf("summarised %q..%q", fa.summarisedFrom, fa.summarisedTo)
	}
	if len(fa.spliced) != 1 || !strings.HasPrefix(fa.spliced[0].Seed, claudeCompactionPrefix) ||
		!strings.Contains(fa.spliced[0].Seed, "it went well") {
		t.Fatalf("splice seed %+v", fa.spliced)
	}
	if len(u.st.AllSummaries()) != 1 {
		t.Fatal("the summary must still be stored for p")
	}
}

func TestAFailedSummaryWritesAndClosesNothing(t *testing.T) {
	fa := &fakeAdapter{summariseErr: errors.New("claude: limit reached"), span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 0 || fa.resumed != "" {
		t.Fatal("something was written or opened after the summary failed")
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "close") {
			t.Fatal("a pane was closed after the summary failed")
		}
	}
	if msg.quit {
		t.Fatal("a failure must stay on screen")
	}
}

func TestAWorkingAgentIsRefusedAtConfirm(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{status: []string{"working"}}), enter, down, enter)
	if cmd != nil || u.confirm != "" {
		t.Fatal("a working agent's session reached the confirmation")
	}
	if !strings.Contains(u.status, "working") || u.m.RangeEnd == nil {
		t.Fatalf("status %q; the range must survive a refusal", u.status)
	}
}

// The summary call takes minutes; the user may start a turn in the old pane
// meanwhile. The summary is kept, nothing is spliced.
func TestAnAgentThatStartsWorkingDuringTheSummaryStopsTheSplice(t *testing.T) {
	fa := &fakeAdapter{summary: "s", span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle", "working"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 0 {
		t.Fatal("spliced while the agent was working")
	}
	if !strings.Contains(msg.status, "summary stored") {
		t.Fatalf("status %q", msg.status)
	}
	if len(u.st.AllSummaries()) != 1 {
		t.Fatal("the paid-for summary was lost")
	}
}

func TestALiveEditOpensTheNewLineFocusedThenClosesTheOld(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, down, enter)
	if !strings.Contains(u.confirm, "Text typed but not sent") {
		t.Fatalf("a live confirmation must warn about the pane closing:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "spliced-sid" || !fa.focused {
		t.Fatalf("resumed %q focused=%v, want the new line with focus", fa.resumed, fa.focused)
	}
	if h.calls[len(h.calls)-1] != "close pane-1" {
		t.Fatalf("herdr calls %v, want the old pane closed last", h.calls)
	}
	if !msg.quit {
		t.Fatalf("a completed handover leaves: %q", msg.status)
	}
}

func TestIfTheNewLineDoesNotOpenTheOldPaneStays(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}, resumeErr: errors.New("split refused")}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	for _, c := range h.calls {
		if strings.HasPrefix(c, "close") {
			t.Fatal("the old pane was closed though the new one never opened")
		}
	}
	if !strings.Contains(msg.status, "old pane left running") || msg.quit {
		t.Fatalf("status %q quit=%v", msg.status, msg.quit)
	}
}

func TestAFailedCloseIsReportedNotUndone(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}, closeErr: errors.New("no such pane")}
	u, _ := press(t, rangeUI(t, fa, h), enter, down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if !strings.Contains(msg.status, "old pane did not close") || msg.quit {
		t.Fatalf("status %q quit=%v", msg.status, msg.quit)
	}
	if u.st.Branches["s"].ReplacedBy == "" {
		t.Fatal("a failed close must not undo the replacement")
	}
}

func TestHerdrNotAnsweringRefusesTheEdit(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{liveErr: errors.New("herdr agent list timed out")}), enter, down, enter)
	if cmd != nil || u.confirm != "" {
		t.Fatal("an edit went ahead without knowing whether a pane holds the session")
	}
}

func TestNoEditStatusCarriesTheSeed(t *testing.T) {
	fa := &fakeAdapter{summary: sentinel, span: adapter.Span{First: 2, Last: 3},
		spliceErr: errors.New("write failed: " + sentinel)}
	u, _ := press(t, rangeUI(t, fa, &herdrLog{}), enter, enter)
	_, cmd := press(t, u, enter)
	if msg := cmd().(actionDoneMsg); strings.Contains(msg.status, sentinel) {
		t.Fatalf("status leaked the summary: %q", msg.status)
	}
}

func pickUI(t *testing.T, fa *fakeAdapter, h *herdrLog) uiModel {
	t.Helper()
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "what the branch found", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	u := uiModel{m: New(session("s", "t1", "t2", "t3")), a: fa, st: st, repoRoot: "/repo",
		live: h.live, closePane: h.close}
	u.m.Cursor = 0
	u, _ = press(t, u, key('p'), enter)
	return u
}

func TestPickingASummaryAwayFromTheLiveTipOffersInsertOrBranch(t *testing.T) {
	u := pickUI(t, &fakeAdapter{}, &herdrLog{})
	if u.menu != "place" {
		t.Fatalf("menu %q, want place", u.menu)
	}
	for _, want := range []string{"insert here", "branch here"} {
		if !strings.Contains(u.View(), want) {
			t.Fatalf("placement menu lacks %q:\n%s", want, u.View())
		}
	}
}

func TestInsertSplicesTheSummaryInAndKeepsWhatFollows(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 1, Last: 1}}
	u, cmd := press(t, pickUI(t, fa, &herdrLog{}), enter)
	if cmd != nil || !strings.Contains(u.confirm, "Everything after it is kept") {
		t.Fatalf("insert must confirm first:\n%s", u.confirm)
	}
	_, cmd = press(t, u, enter)
	cmd()
	if len(fa.spliced) != 1 {
		t.Fatalf("spliced %+v", fa.spliced)
	}
	e := fa.spliced[0]
	if e.After != "t1" || e.From != "" || !strings.HasPrefix(e.Seed, claudeSummaryPrefix) {
		t.Fatalf("edit %+v, want an insert after t1 seeded with the summary", e)
	}
	if u.st.Branches["spliced-sid"].Kind != store.KindInserted {
		t.Fatal("the new record is not marked inserted")
	}
}

func TestBranchHereIsTodaysFoldBack(t *testing.T) {
	fa := &fakeAdapter{}
	u, _ := press(t, pickUI(t, fa, &herdrLog{}), down, enter)
	if !strings.Contains(u.confirm, "NEW session") {
		t.Fatalf("branch here should raise the v2 fold-back confirmation:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	cmd()
	if fa.seededWith == "" || len(fa.spliced) != 0 {
		t.Fatal("branch here must graft, not splice")
	}
	if fa.focused {
		t.Fatal("a branch opens beside the user, unfocused")
	}
}
