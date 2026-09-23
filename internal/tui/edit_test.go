package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

// herdrLog records what the handover asked herdr for, in order.
type herdrLog struct {
	status   []string // successive answers to live(); the last repeats; "" is no pane
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
	if s == "" {
		return "", "", nil
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
	v, last := u.View(), -1
	for _, want := range []string{
		"squash — replace these turns with a summary",
		"squash into… — summarise, put it in another line, drop it here",
		"drop — remove these turns",
	} {
		i := strings.Index(v, want)
		if i <= last {
			t.Fatalf("menu does not offer %q after the one before it:\n%s", want, v)
		}
		last = i
	}
	u, _ = press(t, u, esc)
	if u.menu != "" || u.m.RangeEnd == nil {
		t.Fatal("esc on the menu must close it and keep the range")
	}
}

func TestCutConfirmsOnceThenSplicesAndReplaces(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{}), enter, down, down, enter)
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
	if nb.Kind != store.KindCut || nb.Cut == nil || nb.Cut.Turns != 2 || nb.Cut.At != "t3" || nb.Title != "✂ drop" {
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
	if !fa.summarisedCompact {
		t.Fatal("squash must ask for the compaction prompt")
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
	if msg.quit {
		t.Fatal("a failure must stay on screen")
	}
}

func TestAWorkingAgentIsRefusedAtConfirm(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{status: []string{"working"}}), enter, down, down, enter)
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

func TestHerdrNotAnsweringRefusesTheEdit(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{liveErr: errors.New("herdr agent list timed out")}), enter, down, down, enter)
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
	for _, want := range []string{"merge here", "branch here"} {
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

// p places a summary that already exists: its failure says nothing about one
// being kept.
func TestAFailedPlacementByPSaysNothingAboutAStoredSummary(t *testing.T) {
	fa := &fakeAdapter{seedErr: errors.New("graft refused")}
	u, _ := press(t, pickUI(t, fa, &herdrLog{}), down, enter)
	_, cmd := press(t, u, enter)
	if msg := cmd().(actionDoneMsg); msg.status != "branch failed: graft refused" {
		t.Fatalf("status %q", msg.status)
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
}

// chooseFold picks squash into… from the range menu.
func chooseFold(t *testing.T, u uiModel) uiModel {
	t.Helper()
	u, _ = press(t, u, enter, down, enter)
	return u
}

func TestAFoldCoveringTheWholeLineIsRefusedBeforeItIsPaidFor(t *testing.T) {
	fa := &fakeAdapter{summary: "x", span: adapter.Span{First: 1, Last: 3, Turns: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{}), enter, down, enter)
	if cmd != nil || u.confirm != "" || u.pending != nil || u.folding != nil || u.m.RangeEnd == nil {
		t.Fatalf("want a refusal with the range kept; confirm %q", u.confirm)
	}
	if fa.summarisedFrom != "" {
		t.Fatal("the refusal came after the summary was paid for")
	}
	if u.status != "a squash into… must leave something behind — use p or branch here to copy a whole line" {
		t.Fatalf("status %q", u.status)
	}
}

// moveUI shows two lines, s (turns t1..t3) and o (o1, o2), with t2..t3 of s
// chosen for squash into… and target mode waiting for a turn.
func moveUI(t *testing.T, fa *fakeAdapter, h *herdrLog) uiModel {
	t.Helper()
	if fa.summary == "" {
		fa.summary = "it went well"
	}
	fa.span = adapter.Span{First: 2, Last: 3, Turns: 3}
	st := loadedStore(t)
	roots := tree.Build([]adapter.Session{sessionOf("s", "t1", "t2", "t3"), sessionOf("o", "o1", "o2")}, st)
	u := uiModel{m: New(roots), a: fa, st: st, repoRoot: "/repo", live: h.live, closePane: h.close}
	u = at(t, u, "t3")
	u, _ = press(t, u, key('s'))
	u = chooseFold(t, at(t, u, "t2"))
	if u.folding == nil {
		t.Fatalf("setup: not in target mode: %q", u.status)
	}
	return u
}

// at puts the cursor on the row of node id.
func at(t *testing.T, u uiModel, id string) uiModel {
	t.Helper()
	for i, r := range u.m.Rows() {
		if r.Node.Node.ID == id {
			u.m.Cursor = i
			return u
		}
	}
	t.Fatalf("no row %q", id)
	return u
}

const cutNoteText = "…and turns 2–3 are dropped from s"

// Choosing squash into… pays for nothing and asks herdr nothing: even a
// working source is only asked about once the target is confirmed.
func TestSquashIntoChoosesTheTargetBeforeAnythingIsPaid(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"working"}}
	u := moveUI(t, fa, h)
	if u.confirm != "" || u.pending != nil || fa.summarisedFrom != "" || len(h.calls) != 0 {
		t.Fatalf("choosing squash into… did something: confirm %q summarised %q herdr %v", u.confirm, fa.summarisedFrom, h.calls)
	}
	if u.status != "move to a turn and press ⏎ to squash turns 2–3 into it · esc cancels" {
		t.Fatalf("status %q", u.status)
	}
	if !strings.Contains(u.View(), "⏎ squash into it here") {
		t.Fatalf("the footer does not say what ⏎ does:\n%s", u.View())
	}
	u, cmd := press(t, u, esc)
	if cmd != nil || u.folding != nil || u.quitting {
		t.Fatal("esc must cancel the move, not the overlay")
	}
	if fa.summarisedFrom != "" || len(fa.writes) != 0 || len(u.st.AllSummaries()) != 0 {
		t.Fatalf("esc in target mode paid or wrote: writes %v", fa.writes)
	}
}

func TestAMoveByMergeConfirmsOnceThenSummarisesMergesAndDrops(t *testing.T) {
	fa := &fakeAdapter{}
	u, _ := press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter)
	if u.menu != "place" || !strings.Contains(u.View(), cutNoteText) {
		t.Fatalf("the place menu does not say the source is dropped:\n%s", u.View())
	}
	u, cmd := press(t, u, enter)
	for _, want := range []string{"billed", "Then: the summary is merged into o · turns 2–3 are dropped from s"} {
		if !strings.Contains(u.confirm, want) {
			t.Fatalf("the confirmation lacks %q:\n%s", want, u.confirm)
		}
	}
	if cmd != nil || fa.summarisedFrom != "" || len(fa.writes) != 0 {
		t.Fatalf("paid or wrote before the confirmation: %v", fa.writes)
	}
	u, cmd = press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if got := strings.Join(fa.writes, ","); got != "summarise s,splice o,splice s" {
		t.Fatalf("writes %s, want the summary, then the merge into o, then the drop from s", got)
	}
	if fa.summarisedFrom != "t2" || fa.summarisedTo != "t3" || fa.summarisedCompact {
		t.Fatalf("summarised %q..%q compact %v, want the range with the ordinary prompt", fa.summarisedFrom, fa.summarisedTo, fa.summarisedCompact)
	}
	if e := fa.spliced[0]; e.After != "o1" || !strings.HasPrefix(e.Seed, claudeSummaryPrefix) || !strings.Contains(e.Seed, "it went well") {
		t.Fatalf("the merge %+v", e)
	}
	if e := fa.spliced[1]; e != (adapter.Edit{From: "t2", To: "t3"}) {
		t.Fatalf("the drop %+v, want exactly the summarised range", e)
	}
	if len(u.st.AllSummaries()) != 1 {
		t.Fatal("the summary must be stored for p")
	}
	if u.st.Branches["o"].ReplacedBy != "spliced-sid" || u.st.Branches["s"].ReplacedBy != "spliced2-sid" {
		t.Fatalf("store %+v", u.st.Branches)
	}
	if b := u.st.Branches["spliced2-sid"]; b.Kind != store.KindCut || b.Cut == nil || b.Cut.Turns != 2 || b.Cut.At != "t3" || b.Title != "✂ drop" {
		t.Fatalf("drop record %+v", b)
	}
	if msg.status != "squashed into o, dropped 2 turns from s → spliced2" || !msg.reload || msg.quit || msg.tip != "spliced-sid" {
		t.Fatalf("%+v", msg)
	}
	if u.folding != nil {
		t.Fatal("target mode outlived the move")
	}
}

func TestAMoveByBranchSummarisesGraftsThenDrops(t *testing.T) {
	fa := &fakeAdapter{}
	u, _ := press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, down, enter)
	for _, want := range []string{"billed", "Then: a new line branches at o, carrying the summary · turns 2–3 are dropped from s"} {
		if !strings.Contains(u.confirm, want) {
			t.Fatalf("the confirmation lacks %q:\n%s", want, u.confirm)
		}
	}
	if fa.summarisedFrom != "" {
		t.Fatal("summarised before the confirmation")
	}
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if got := strings.Join(fa.writes, ","); got != "summarise s,graft o,splice s" {
		t.Fatalf("writes %s, want the summary, the graft, then the drop", got)
	}
	if !strings.Contains(fa.seededWith, "it went well") {
		t.Fatalf("the graft's seed %q lacks the summary", fa.seededWith)
	}
	if msg.status != "squashed into o, dropped 2 turns from s → spliced-" || !msg.reload || msg.tip != "new-sid" {
		t.Fatalf("%+v", msg)
	}
}

func TestAMoveToTheLiveTipConfirmsThenSummarisesSendsDropsAndQuits(t *testing.T) {
	fa := &fakeAdapter{}
	u := moveUI(t, fa, &herdrLog{})
	u.current, u.liveAgent = "o", "agent-1"
	var sent string
	u.send = func(agent, text string) error {
		fa.writes = append(fa.writes, "send "+agent)
		sent = text
		return nil
	}
	u, cmd := press(t, at(t, u, "o2"), enter)
	for _, want := range []string{"billed", "Then: the summary is sent to agent-1 as your next message · turns 2–3 are dropped from s"} {
		if !strings.Contains(u.confirm, want) {
			t.Fatalf("the confirmation lacks %q:\n%s", want, u.confirm)
		}
	}
	if cmd != nil || fa.summarisedFrom != "" || len(fa.writes) != 0 {
		t.Fatalf("sent or paid before the confirmation: %v", fa.writes)
	}
	_, cmd = press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if got := strings.Join(fa.writes, ","); got != "summarise s,send agent-1,splice s" {
		t.Fatalf("writes %s, want the summary, the send, then the drop", got)
	}
	if !strings.Contains(sent, "it went well") {
		t.Fatalf("sent %q, want the summary", sent)
	}
	if !msg.quit || msg.status != "squashed into o, dropped 2 turns from s → spliced-" {
		t.Fatalf("%+v", msg)
	}
}

func TestAFailedMoveSummaryWritesAndDropsNothing(t *testing.T) {
	fa := &fakeAdapter{summariseErr: errors.New("claude: limit reached")}
	u, _ := press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, enter)
	u, cmd := press(t, u, enter)
	next, _ := u.Update(cmd())
	u = next.(uiModel)
	if got := strings.Join(fa.writes, ","); got != "summarise s" {
		t.Fatalf("writes %s, want only the failed summary", got)
	}
	if u.quitting || len(u.st.AllSummaries()) != 0 || !strings.Contains(u.status, "summarise failed") {
		t.Fatalf("status %q", u.status)
	}
}

func TestAFailedFoldCutsNothing(t *testing.T) {
	fa := &fakeAdapter{seedErr: errors.New("graft refused")}
	u, _ := press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if got := strings.Join(fa.writes, ","); got != "summarise s,graft o" || msg.status != "summary stored — branch failed: graft refused" {
		t.Fatalf("writes %s status %q", got, msg.status)
	}

	fa = &fakeAdapter{spliceErrAt: 1}
	u, _ = press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, enter)
	_, cmd = press(t, u, enter)
	msg = cmd().(actionDoneMsg)
	if got := strings.Join(fa.writes, ","); got != "summarise s,splice o" || msg.status != "summary stored — merged failed: disk full" {
		t.Fatalf("writes %s status %q", got, msg.status)
	}
}

func TestACutThatFailsAfterTheFoldSaysSo(t *testing.T) {
	fa := &fakeAdapter{spliceErrAt: 2}
	u, _ := press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, enter)
	u, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if msg.status != "squashed into o, but the source was not dropped: drop failed: disk full" || !msg.reload || msg.quit {
		t.Fatalf("%+v", msg)
	}
	if u.st.Branches["o"].ReplacedBy != "spliced-sid" || u.st.Branches["s"].ReplacedBy != "" {
		t.Fatalf("the fold's record must stand and the source stay: %+v", u.st.Branches)
	}
}

// The summary takes minutes; the source's agent may start a turn meanwhile,
// and dropping then would hide the line it runs on.
func TestAMoveChecksTheSourceAgainBeforeCutting(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"idle", "working"}}
	u, _ := press(t, at(t, moveUI(t, fa, h), "o1"), enter, down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if got := strings.Join(fa.writes, ","); got != "summarise s,graft o" {
		t.Fatalf("writes %s, want the fold and no cut", got)
	}
	if got := strings.Join(h.calls, ","); got != "live s,live s" {
		t.Fatalf("herdr calls %s, want the source asked at confirm and again before the cut", got)
	}
	if msg.status != "squashed into o, but the source was not dropped: agent is working — wait for it to finish; nothing was written" ||
		!msg.reload || msg.quit || msg.tip != "new-sid" {
		t.Fatalf("%+v", msg)
	}
}

func TestFoldingIntoTheSourceLineIsRefused(t *testing.T) {
	fa := &fakeAdapter{}
	u, cmd := press(t, at(t, moveUI(t, fa, &herdrLog{}), "t1"), enter)
	if cmd != nil || u.menu != "" || u.folding == nil || len(fa.writes) != 0 {
		t.Fatalf("menu %q folding %v writes %v", u.menu, u.folding, fa.writes)
	}
	if u.status != "merge into another line — use squash for this one" {
		t.Fatalf("status %q", u.status)
	}
}

func TestABusySourceAtConfirmPaysForNothing(t *testing.T) {
	for _, h := range []*herdrLog{{status: []string{"working"}}, {liveErr: errors.New("timed out")}} {
		fa := &fakeAdapter{}
		u, _ := press(t, at(t, moveUI(t, fa, h), "o1"), enter, down, enter)
		_, cmd := press(t, u, enter)
		msg := cmd().(actionDoneMsg)
		if fa.summarisedFrom != "" || len(fa.writes) != 0 {
			t.Fatalf("writes %v, want nothing summarised or written", fa.writes)
		}
		if strings.Join(h.calls, ",") != "live s" {
			t.Fatalf("herdr calls %v, want the source asked about", h.calls)
		}
		if msg.status != "the source's agent is working — wait for it to finish; nothing was paid or written" &&
			msg.status != "cannot tell whether the source is busy: timed out — nothing was paid or written" {
			t.Fatalf("status %q", msg.status)
		}
	}
}

// esc on the place menu or the confirmation cancels the move, and a summary
// p later places moves nothing.
func TestEscCancelsTheMoveAndPNeverCuts(t *testing.T) {
	fa := &fakeAdapter{}
	u, _ := press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, esc)
	if u.menu != "" || u.folding != nil || u.status != "squash into… cancelled — nothing was paid or written" {
		t.Fatalf("esc on the place menu kept the move: menu %q status %q", u.menu, u.status)
	}
	u, _ = press(t, at(t, moveUI(t, fa, &herdrLog{}), "o1"), enter, enter, esc)
	if u.confirm != "" || u.folding != nil || u.status != "squash into… cancelled — nothing was paid or written" {
		t.Fatalf("esc on the confirmation kept the move: status %q", u.status)
	}
	if fa.summarisedFrom != "" || len(fa.writes) != 0 || len(u.st.AllSummaries()) != 0 {
		t.Fatalf("a cancelled move paid or wrote: %v", fa.writes)
	}
	u.st.AddSummary(store.Summary{Text: "what the branch found", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	u, _ = press(t, at(t, u, "o1"), key('p'), enter)
	if strings.Contains(u.View(), cutNoteText) {
		t.Fatalf("p's place menu promises a cut:\n%s", u.View())
	}
	u, _ = press(t, u, enter)
	_, cmd := press(t, u, enter)
	cmd()
	if got := strings.Join(fa.writes, ","); got != "splice o" {
		t.Fatalf("writes %s, want only the merge", got)
	}
}

func TestContinueOnALiveSessionOpensAndClosesNothing(t *testing.T) {
	fa := &fakeAdapter{summary: "x", span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, enter)
	if strings.Contains(u.confirm, "Text typed but not sent") {
		t.Fatalf("an edit closes no pane, so it must not warn of one:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "" {
		t.Fatalf("an edit opened %q", fa.resumed)
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "close") {
			t.Fatalf("an edit closed a pane: %v", h.calls)
		}
	}
	if !msg.reload || msg.quit || msg.status != "squashed s → spliced- — ⏎ on it to continue there" {
		t.Fatalf("%+v", msg)
	}
}

func TestCutSaysHowManyTurnsWent(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, _ := press(t, rangeUI(t, fa, &herdrLog{}), enter, down, down, enter)
	_, cmd := press(t, u, enter)
	if msg := cmd().(actionDoneMsg); msg.status != "dropped 2 turns from s → spliced- — ⏎ on it to continue there" {
		t.Fatalf("status %q", msg.status)
	}
}

func sessionOf(id string, turns ...string) adapter.Session {
	s := adapter.Session{ID: id, Title: id, CWD: "/repo", Path: "/transcripts/" + id + ".jsonl"}
	for _, t := range turns {
		s.Nodes = append(s.Nodes, adapter.Node{ID: t, Title: "turn " + t, Kind: adapter.KindHuman})
	}
	return s
}

// cutAndReload cuts t2..t3 of s and feeds the result back, with Discover
// finding the replacement (which gained a turn) and an unrelated session.
func cutAndReload(t *testing.T, current string) uiModel {
	t.Helper()
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}, sessions: []adapter.Session{
		sessionOf("s", "t1", "t2", "t3"), sessionOf("spliced-sid", "t1", "t3", "t4"), sessionOf("other", "o1")}}
	u := rangeUI(t, fa, &herdrLog{})
	u.current = current
	u, _ = press(t, u, enter, down, down, enter)
	u, cmd := press(t, u, enter)
	next, _ := u.Update(cmd())
	return next.(uiModel)
}

func TestAReloadPutsTheCursorOnTheNewLinesTip(t *testing.T) {
	u := cutAndReload(t, "")
	if n := u.m.Selected(); n == nil || n.SessionID != "spliced-sid" || !n.IsSessionLeaf {
		t.Fatalf("cursor on %+v, want the replacement's tip", n)
	}
}

func TestScopeFollowsTheReplacementOfTheCurrentSession(t *testing.T) {
	u := cutAndReload(t, "s")
	rows := u.m.Rows()
	if len(rows) == 0 {
		t.Fatal("nothing on screen")
	}
	for _, r := range rows {
		if r.Node.SessionID != "spliced-sid" {
			t.Fatalf("scope fell back to all sessions: row of %q", r.Node.SessionID)
		}
	}
}

func TestBranchHereWritesAndOpensNothing(t *testing.T) {
	fa := &fakeAdapter{}
	u, _ := press(t, pickUI(t, fa, &herdrLog{}), down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.seededWith == "" || fa.resumed != "" {
		t.Fatalf("branch here must write and open nothing: resumed %q", fa.resumed)
	}
	if !msg.reload || msg.quit || msg.status != "branched new-sid — ⏎ on it to open it" {
		t.Fatalf("%+v", msg)
	}
}

// replacedUI shows spliced-sid, which replaced s, with the cursor on its tip.
func replacedUI(t *testing.T, fa *fakeAdapter, h *herdrLog) uiModel {
	t.Helper()
	st := loadedStore(t)
	st.Replace("s", "spliced-sid", store.Branch{Kind: store.KindCut})
	roots := tree.Build([]adapter.Session{sessionOf("s", "t1", "t2", "t3"), sessionOf("spliced-sid", "t1", "t3")}, st)
	u := uiModel{m: New(roots), a: fa, st: st, repoRoot: "/repo", live: h.live,
		closePane: func(p string) error {
			if fa.resumed == "" {
				h.calls = append(h.calls, "close-before-resume")
			}
			return h.close(p)
		}}
	u.m.Cursor = len(u.m.Rows()) - 1
	if n := u.m.Selected(); n.SessionID != "spliced-sid" || !n.IsSessionLeaf {
		t.Fatalf("setup: cursor on %+v", n)
	}
	return u
}

func TestEnterOnAReplacementHandsTheOldPaneOver(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"", "idle"}}
	u, cmd := press(t, replacedUI(t, fa, h), enter)
	if cmd != nil || !strings.Contains(u.confirm, "text typed but not sent there is lost") {
		t.Fatalf("want a confirmation first:\n%s", u.confirm)
	}
	_, cmd = press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "spliced-sid" || !fa.focused {
		t.Fatalf("resumed %q focused=%v", fa.resumed, fa.focused)
	}
	if got := strings.Join(h.calls, ","); got != "live spliced-sid,live s,live s,close pane-1" {
		t.Fatalf("herdr calls %s, want the old pane closed after the open", got)
	}
	if !msg.quit || msg.status != "opened spliced-, old pane closed" {
		t.Fatalf("%+v", msg)
	}
}

func TestEnterOnAReplacementWhoseOldAgentWorksIsRefused(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"", "working"}}
	u, cmd := press(t, replacedUI(t, fa, h), enter)
	if cmd != nil || u.confirm != "" || !strings.Contains(u.status, "working") {
		t.Fatalf("status %q confirm %q", u.status, u.confirm)
	}
	if fa.resumed != "" || strings.Contains(strings.Join(h.calls, ","), "close") {
		t.Fatal("something was opened or closed")
	}
}

func TestIfTheReplacementDoesNotOpenTheOldPaneStays(t *testing.T) {
	fa := &fakeAdapter{resumeErr: errors.New("split refused")}
	h := &herdrLog{status: []string{"", "idle"}}
	u, _ := press(t, replacedUI(t, fa, h), enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if strings.Contains(strings.Join(h.calls, ","), "close") {
		t.Fatalf("the old pane was closed though the new one never opened: %v", h.calls)
	}
	if !strings.Contains(msg.status, "old pane left running") || msg.quit {
		t.Fatalf("%+v", msg)
	}
}

func TestEnterOnAReplacementWithNoOldPaneJustResumes(t *testing.T) {
	fa := &fakeAdapter{}
	u, cmd := press(t, replacedUI(t, fa, &herdrLog{}), enter)
	if cmd == nil || u.confirm != "" {
		t.Fatalf("want a plain resume, got confirm %q", u.confirm)
	}
	cmd()
	if fa.resumed != "spliced-sid" || fa.focused {
		t.Fatalf("resumed %q focused=%v, want unfocused", fa.resumed, fa.focused)
	}
}

func TestAFailedCloseAfterTheHandoverIsReported(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"", "idle"}, closeErr: errors.New("no such pane")}
	u, _ := press(t, replacedUI(t, fa, h), enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "spliced-sid" || !fa.focused {
		t.Fatalf("resumed %q focused=%v", fa.resumed, fa.focused)
	}
	if msg.quit || msg.status != "opened spliced-, but the old pane did not close: no such pane" {
		t.Fatalf("%+v", msg)
	}
}

func TestEnterOnAReplacementWhenHerdrWillNotSayIsRefused(t *testing.T) {
	fa := &fakeAdapter{}
	u, cmd := press(t, replacedUI(t, fa, &herdrLog{liveErr: errors.New("herdr agent list timed out")}), enter)
	if cmd != nil || u.confirm != "" || fa.resumed != "" {
		t.Fatalf("went ahead without knowing: confirm %q resumed %q", u.confirm, fa.resumed)
	}
	if !strings.HasPrefix(u.status, "cannot tell whether") {
		t.Fatalf("status %q", u.status)
	}
}

func TestABlockedAgentRefusesAnEdit(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{status: []string{"blocked"}}), enter, down, down, enter)
	if cmd != nil || u.confirm != "" || u.status != "agent is blocked — wait for it to finish" {
		t.Fatalf("status %q confirm %q", u.status, u.confirm)
	}
}

func TestABlockedOldAgentRefusesTheHandover(t *testing.T) {
	fa := &fakeAdapter{}
	u, cmd := press(t, replacedUI(t, fa, &herdrLog{status: []string{"", "blocked"}}), enter)
	if cmd != nil || u.confirm != "" || u.status != "the old line's agent is blocked — wait for it to finish" {
		t.Fatalf("status %q confirm %q", u.status, u.confirm)
	}
}

// The confirmation can sit on screen for as long as the user likes; the old
// agent may start a turn meanwhile, and closing its pane would kill it.
func TestTheHandoverChecksTheOldPaneAgainBeforeClosingIt(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"", "idle", "working"}}
	u, _ := press(t, replacedUI(t, fa, h), enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "spliced-sid" {
		t.Fatalf("resumed %q", fa.resumed)
	}
	if strings.Contains(strings.Join(h.calls, ","), "close") {
		t.Fatalf("closed a pane whose agent was working: %v", h.calls)
	}
	if msg.quit || msg.status != "opened spliced- — old pane left running: its agent is working" {
		t.Fatalf("%+v", msg)
	}
}

// If the old pane closed on its own between the confirmation and the
// handover's recheck, live() reports no pane (or a different one) rather
// than "idle" — the handover is still done, since a new pane is already
// open, but there is no old pane left to close.
func TestTheHandoverFindsTheOldPaneAlreadyGone(t *testing.T) {
	fa := &fakeAdapter{}
	h := &herdrLog{status: []string{"", "idle", ""}}
	u, _ := press(t, replacedUI(t, fa, h), enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "spliced-sid" {
		t.Fatalf("resumed %q", fa.resumed)
	}
	if strings.Contains(strings.Join(h.calls, ","), "close") {
		t.Fatalf("closed a pane that was already gone: %v", h.calls)
	}
	if !msg.quit || msg.status != "opened spliced- — the old pane is already gone" {
		t.Fatalf("%+v", msg)
	}
}

func TestAPaneOpenedDuringTheSummaryStopsTheSplice(t *testing.T) {
	fa := &fakeAdapter{summary: "s", span: adapter.Span{First: 2, Last: 3}}
	u, _ := press(t, rangeUI(t, fa, &herdrLog{status: []string{"", "working"}}), enter, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 0 || !strings.HasPrefix(msg.status, "summary stored") {
		t.Fatalf("spliced %+v status %q", fa.spliced, msg.status)
	}
}

func TestHerdrFailingAfterTheSummarySaysTheSummaryWasStored(t *testing.T) {
	roots := session("s", "t1", "t2")
	rows := New(roots).Rows()
	op := editOp{src: adapter.Session{ID: "s"}, kind: store.KindCompacted, summarise: true, from: rows[0].Node, to: rows[1].Node}
	fa := &fakeAdapter{summary: "x"}
	live := func(string) (string, string, error) { return "", "", errors.New("timed out") }
	msg := editCmd(fa, loadedStore(t), op, live)().(actionDoneMsg)
	if len(fa.spliced) != 0 || msg.status != "summary stored — cannot tell whether the session is busy: timed out; nothing was spliced" {
		t.Fatalf("spliced %+v status %q", fa.spliced, msg.status)
	}
}

func TestAMissingReplacementKeepsTheCurrentScope(t *testing.T) {
	st := loadedStore(t)
	st.Replace("s", "gone", store.Branch{Kind: store.KindCut})
	u := uiModel{m: New(nil), st: st, current: "s",
		roots: tree.Build([]adapter.Session{sessionOf("s", "t1"), sessionOf("other", "o1")}, st)}
	u.rebuild()
	rows := u.m.Rows()
	if len(rows) == 0 {
		t.Fatal("nothing on screen")
	}
	for _, r := range rows {
		if r.Node.SessionID != "s" {
			t.Fatalf("scope fell back to all sessions: row of %q", r.Node.SessionID)
		}
	}
}

func TestEnterOnATipAlreadyOpenInAPaneOpensNoSecondAgent(t *testing.T) {
	fa := &fakeAdapter{}
	u := uiModel{m: New(session("s", "t1", "t2")), a: fa, st: loadedStore(t), repoRoot: "/repo",
		live: (&herdrLog{status: []string{"idle"}}).live}
	u.m.Cursor = 1
	u, cmd := press(t, u, enter)
	if cmd != nil || fa.resumed != "" || u.status != "already open in pane pane-1" {
		t.Fatalf("status %q resumed %q", u.status, fa.resumed)
	}
}
