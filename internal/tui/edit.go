package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

// LiveFunc reports the pane holding a session and herdr's agent_status for
// it; both empty when no pane holds it. ClosePaneFunc closes a pane. Both are
// injected, like SendFunc, so the handover is testable without Herdr.
type (
	LiveFunc      func(sessionID string) (pane, status string, err error)
	ClosePaneFunc func(paneID string) error
)

// editOp is one context edit, captured when its confirmation is raised.
type editOp struct {
	src       adapter.Session
	edit      adapter.Edit
	kind      string     // store.KindCompacted | KindCut | KindInserted
	summarise bool       // compact: the seed is produced first
	from, to  *tree.Node // compact: the range to summarise
	title     string     // row title for the new record
	pane      string     // pane holding src at confirm time, for the busy re-check; "" if none
	dst       string
}

// summariseRange makes the billed summary call over op's range and stores
// the result, so p can fold it in later (§2.6). On failure the status says
// why and that nothing was written.
func summariseRange(a adapter.Adapter, st *store.Store, op editOp) (store.Summary, string) {
	text, err := a.Summarise(op.src, op.from.Node.ID, op.to.Node.ID)
	if err != nil {
		return store.Summary{}, "summarise failed: " + err.Error() + " — nothing was written"
	}
	sum := store.Summary{Text: text, SessionID: op.src.ID,
		FromTurn: op.from.Node.ID, ToTurn: op.to.Node.ID, CreatedAt: time.Now().UTC()}
	st.AddSummary(sum)
	if err := st.Save(); err != nil {
		return store.Summary{}, "summarised, but not saved: " + err.Error() + " — nothing was written"
	}
	return sum, ""
}

// foldCmd is summarise & fold: the summary is made and stored, and the
// overlay then waits in fold mode for the user to say where it goes (§2.7).
func foldCmd(a adapter.Adapter, st *store.Store, op editOp) tea.Cmd {
	return func() tea.Msg {
		sum, failed := summariseRange(a, st, op)
		if failed != "" {
			return actionDoneMsg{status: failed}
		}
		return actionDoneMsg{status: "summary ready — move to a turn and press ⏎ to fold it in · esc keeps it for later (p)", fold: &sum}
	}
}

// editCmd runs an edit to completion or to its first failure, in the order
// the spec fixes (§6.1). Each step runs only if the one before succeeded, and
// every status says what DID happen. It only writes: moving to the new line
// is the user's own ⏎ (§6.2).
func editCmd(a adapter.Adapter, st *store.Store, op editOp, live LiveFunc) tea.Cmd {
	return func() tea.Msg {
		if op.summarise {
			sum, failed := summariseRange(a, st, op)
			if failed != "" {
				return actionDoneMsg{status: failed}
			}
			op.edit.Seed = foldBackSeed(op.from, sum, true)
			op.title = "⤶ " + title(sum.Text, 40)
		}
		if op.pane != "" {
			// The summary call takes minutes. Whatever was true at confirm
			// time is checked again right before the transcript is read.
			_, status, err := live(op.src.ID)
			if err != nil {
				return actionDoneMsg{status: "cannot tell whether the session is busy: " + scrubbed(err, op.edit.Seed) + " — nothing was written"}
			}
			if status == "working" {
				if op.summarise {
					return actionDoneMsg{status: "summary stored — agent is busy; select again or use p"}
				}
				return actionDoneMsg{status: "agent is working — wait for it to finish; nothing was written"}
			}
		}
		res, err := a.Splice(op.src, op.edit, op.dst)
		if err != nil {
			return actionDoneMsg{status: op.kind + " failed: " + scrubbed(err, op.edit.Seed)}
		}
		b := store.Branch{Kind: op.kind, Title: op.title, CreatedAt: time.Now().UTC()}
		if op.kind == store.KindCut {
			b.Cut = &store.Cut{Turns: res.Removed, At: res.After}
		}
		st.Replace(op.src.ID, res.SessionID, b)
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + ", but the tree was not saved: " + err.Error()}
		}
		what := op.kind
		if op.kind == store.KindCut {
			what = fmt.Sprintf("cut %d turns from", res.Removed)
		}
		return actionDoneMsg{status: what + " " + shortID(op.src.ID) + " → " + shortID(res.SessionID) + " — ⏎ on it to continue there",
			reload: true, tip: res.SessionID}
	}
}

// handoverCmd opens a replacement with focus and only then closes the pane
// still running the line it replaced (§6.2): if the open fails, the old pane
// is all the user has, so it stays.
func handoverCmd(a adapter.Adapter, sid, dst, oldPane string, closePane ClosePaneFunc) tea.Cmd {
	return func() tea.Msg {
		if err := a.Resume(sid, dst, true); err != nil {
			return actionDoneMsg{status: "could not open " + shortID(sid) + " — old pane left running: " + err.Error()}
		}
		if err := closePane(oldPane); err != nil {
			return actionDoneMsg{status: "opened " + shortID(sid) + ", but the old pane did not close: " + err.Error()}
		}
		return actionDoneMsg{status: "opened " + shortID(sid) + ", old pane closed", quit: true}
	}
}

// openTip is ⏎ on a line's tip. If a line it replaced is still open in a
// pane, that pane is handed over after a confirmation; otherwise it is a
// plain resume, with nothing to confirm.
func (u uiModel) openTip(n *tree.Node) (tea.Model, tea.Cmd) {
	if u.live != nil && u.st != nil {
		seen := map[string]bool{n.SessionID: true}
		for old := u.st.Branches[n.SessionID].Replaces; old != "" && !seen[old]; old = u.st.Branches[old].Replaces {
			seen[old] = true
			pane, status, err := u.live(old)
			if err != nil {
				u.status = "cannot tell whether the old line is still open: " + err.Error()
				return u, nil
			}
			if pane == "" {
				continue
			}
			if status == "working" {
				u.status = "the old line's agent is working — wait for it to finish"
				return u, nil
			}
			u.confirm = fmt.Sprintf("Continue on the new line:  %q\n\nThe pane running the old line is closed; text typed but not sent there is lost.\n\n[enter] continue   [esc] back", n.Node.Title)
			u.pending, u.pendingBusy = handoverCmd(u.a, n.SessionID, u.dstCWD(n), pane, u.closePane), "opening session…"
			return u, nil
		}
	}
	u.busy = "opening session…"
	return u, resumeCmd(u.a, n, u.dstCWD(n))
}

const replacesLine = "A new session replaces this line in the tree (the old one is hidden, kept on disk)."

// kindFold names summarise & fold in editConfirm. It is not a store kind:
// fold writes nothing to the line it summarises.
const kindFold = "fold"

// editConfirm raises the one confirmation for a range option (§2.3). kind is
// store.KindCompacted (summarise & continue), kindFold or store.KindCut.
func (u uiModel) editConfirm(kind string) (tea.Model, tea.Cmd) {
	from, to, ok := u.m.RangeSpan()
	if !ok {
		u.m.CancelRange()
		u.status = "the range is no longer on screen"
		return u, nil
	}
	if from.SessionID != to.SessionID {
		u.status = "a range must stay inside one session"
		return u, nil
	}
	src := adapter.Session{ID: to.SessionID, CWD: to.SessionCWD, Path: to.SessionPath}
	var pane string
	if kind != kindFold {
		// Fold never touches this line, so its agent may be doing anything.
		if pane, ok = u.liveCheck(src.ID); !ok {
			return u, nil
		}
	}
	sp, err := u.a.Widen(src, from.Node.ID, to.Node.ID)
	if err != nil {
		u.status = "cannot edit this range: " + err.Error()
		return u, nil
	}
	op := editOp{src: src, edit: adapter.Edit{From: from.Node.ID, To: to.Node.ID}, kind: kind,
		from: from, to: to, pane: pane, dst: u.dstCWD(to), title: "✂ " + from.Node.Title}
	if kind == store.KindCut {
		text := fmt.Sprintf("Cut turns %d–%d:\n\n  from  %q\n  to    %q\n\nRemoves turns %d–%d. Costs nothing. No note is left in the conversation.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, sp.First, sp.Last, replacesLine)
		u.confirm = text + "\n\n[enter] go   [esc] back"
		u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "cutting…"
		return u, nil
	}
	turns, entries, size, err := u.a.Preview(src, sp.End)
	if err != nil {
		u.status = "cannot summarise this range: " + err.Error()
		return u, nil
	}
	heading, then := "continue", fmt.Sprintf("turns %d–%d are replaced by the summary.\n%s", sp.First, sp.Last, replacesLine)
	if kind == kindFold {
		heading, then = "fold", "you choose where to fold it in. Nothing is written to this line."
	}
	text := fmt.Sprintf("Summarise & %s turns %d–%d:\n\n  from  %q\n  to    %q\n\nThe model reads this session up to the end of the range — %d turn(s) · %d entries · %s — and describes only the range. That whole prefix is billed.\n\nThen: %s",
		heading, sp.First, sp.Last, from.Node.Title, to.Node.Title, turns, entries, humanBytes(size), then)
	u.confirm = text + "\n\n[enter] go   [esc] back"
	op.summarise = true
	u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "summarising…"
	if kind == kindFold {
		u.pending = foldCmd(u.a, u.st, op)
	}
	return u, nil
}

// liveCheck resolves which pane holds sessionID. ok=false means the edit is
// refused and u.status says why: a working agent, or a herdr that will not
// say — an edit must know whether a turn is running under it, not guess.
func (u *uiModel) liveCheck(sessionID string) (pane string, ok bool) {
	if u.live == nil {
		return "", true
	}
	pane, status, err := u.live(sessionID)
	if err != nil {
		u.status = "cannot tell whether this session is open: " + err.Error()
		return "", false
	}
	if status == "working" {
		u.status = "agent is working — wait for it to finish"
		return "", false
	}
	return pane, true
}

var rangeMenu = []string{"summarise & continue", "summarise & fold", "cut"}
var placeMenu = []string{"insert here", "branch here"}

func menuView(heading string, options []string, idx int) string {
	s := heading + "\n\n"
	for i, o := range options {
		marker := "  "
		if i == idx {
			marker = "> "
		}
		s += marker + o + "\n"
	}
	return s + "\n↑↓ choose   [enter] select   [esc] back\n"
}

// openRangeMenu raises the range menu over the fixed range: §2.2. It refuses
// the same way editConfirm does when the range no longer applies, since the
// same checks are needed before either option can be offered.
func (u uiModel) openRangeMenu() (tea.Model, tea.Cmd) {
	from, to, ok := u.m.RangeSpan()
	if !ok {
		u.m.CancelRange()
		u.status = "the range is no longer on screen"
		return u, nil
	}
	if from.SessionID != to.SessionID {
		u.status = "a range must stay inside one session"
		return u, nil
	}
	u.menu, u.menuIdx = "range", 0
	return u, nil
}

// placeChosen acts on the placement menu. Insert rewrites the line in place
// and hides the old one; branch is v2's seeded graft and leaves both visible.
func (u uiModel) placeChosen(idx int) (tea.Model, tea.Cmd) {
	at, sum := u.pickAt, u.placing
	u.pickAt = nil
	src := adapter.Session{ID: at.SessionID, CWD: at.SessionCWD, Path: at.SessionPath}
	if idx == 1 {
		turns, entries, size, err := u.a.Preview(src, at.Node.ID)
		if err != nil {
			u.status = "cannot fold back here: " + err.Error()
			return u, nil
		}
		u.confirm = foldBackConfirmText(at, turns, entries, size)
		u.pending = foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, u.agentFor(at), u.send)
		u.pendingBusy = "folding back…"
		return u, nil
	}
	pane, ok := u.liveCheck(at.SessionID)
	if !ok {
		return u, nil
	}
	sp, err := u.a.Widen(src, at.Node.ID, at.Node.ID)
	if err != nil {
		u.status = "cannot insert here: " + err.Error()
		return u, nil
	}
	// Nothing is removed, so nothing contracted: an insert is always marked as
	// knowledge arriving, whatever session the summary came from.
	seed := foldBackSeed(at, sum, false)
	op := editOp{src: src, edit: adapter.Edit{After: at.Node.ID, Seed: seed}, kind: store.KindInserted,
		pane: pane, dst: u.dstCWD(at), title: "⤶ " + title(sum.Text, 40)}
	u.confirm = fmt.Sprintf("Insert the summary after turn %d:  %q\n\nEverything after it is kept. Costs nothing.\n%s\n\n[enter] insert   [esc] back", sp.Last, at.Node.Title, replacesLine)
	u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "inserting…"
	return u, nil
}

// foldAt is choosing sum for turn at, from p's picker or from fold mode: at
// the live tip it is the next message, anywhere else the place menu (§2.5).
func (u uiModel) foldAt(at *tree.Node, sum store.Summary) (tea.Model, tea.Cmd) {
	if agent := u.agentFor(at); agent != "" && at.IsSessionLeaf && u.send != nil {
		// Nothing is copied and nothing is written: the summary is the next
		// message. There is no cost to show.
		u.busy = "sending…"
		return u, foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, agent, u.send)
	}
	u.placing, u.pickAt = sum, at
	u.menu, u.menuIdx = "place", 0
	return u, nil
}
