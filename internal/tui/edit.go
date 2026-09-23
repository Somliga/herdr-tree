package tui

import (
	"errors"
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

// busy reports whether an agent_status forbids touching the session under
// it. Only idle is safe: working, blocked, or a status herdr adds later all
// mean a turn may be running. "" is no agent at all.
func busy(status string) bool { return status != "" && status != "idle" }

// editOp is one context edit, captured when its confirmation is raised.
type editOp struct {
	src       adapter.Session
	edit      adapter.Edit
	kind      string     // store.KindCompacted | KindCut | KindInserted
	summarise bool       // compact: the seed is produced first
	from, to  *tree.Node // compact: the range to summarise
	title     string     // row title for the new record
	dst       string
}

// summariseRange makes the billed summary call over op's range and stores
// the result, so p can fold it in later (§2.6). On failure the status says
// why and that nothing was written.
func summariseRange(a adapter.Adapter, st *store.Store, op editOp) (store.Summary, string) {
	text, err := a.Summarise(op.src, op.from.Node.ID, op.to.Node.ID, op.kind == store.KindCompacted)
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

// foldMove is fold mode's hand: the summary, and the cut of the range it was
// made from, written once the summary has landed (§2.7).
type foldMove struct {
	sum         store.Summary
	cut         editOp
	first, last int // the widened range, for the texts
}

// note is what the place menu and every confirmation on the move add.
func (mv *foldMove) note() string {
	if mv == nil {
		return ""
	}
	return fmt.Sprintf("\n…and turns %d–%d are cut from %s", mv.first, mv.last, shortID(mv.cut.src.ID))
}

// foldCmd is summarise & fold: the summary is made and stored, and the
// overlay then waits in fold mode for the user to say where it goes (§2.7).
func foldCmd(a adapter.Adapter, st *store.Store, op editOp, mv foldMove) tea.Cmd {
	return func() tea.Msg {
		sum, failed := summariseRange(a, st, op)
		if failed != "" {
			return actionDoneMsg{status: failed}
		}
		mv.sum = sum
		return actionDoneMsg{status: "summary ready — move to a turn and press ⏎ to fold it in · esc keeps it for later (p)", fold: &mv}
	}
}

// cutAfter runs fold and, only if it landed, cuts mv's range from its source.
// The fold comes first: if the cut then fails, the stretch is in two places,
// never in none. target is the session the user folded into.
func cutAfter(fold tea.Cmd, a adapter.Adapter, st *store.Store, mv foldMove, target string) tea.Cmd {
	return func() tea.Msg {
		msg := fold().(actionDoneMsg)
		// A fold reloads or quits only when it wrote and recorded; anything
		// else is its failure, and the source stays as it is.
		if !msg.reload && !msg.quit {
			return msg
		}
		res, failed := writeEdit(a, st, mv.cut)
		if failed != "" {
			tip := msg.tip
			if tip == "" {
				tip = target
			}
			return actionDoneMsg{status: "folded into " + shortID(target) + ", but the source was not cut: " + scrubbed(errors.New(failed), mv.sum.Text), reload: true, tip: tip}
		}
		msg.status = fmt.Sprintf("folded into %s, cut %d turns from %s → %s", shortID(target), res.Removed, shortID(mv.cut.src.ID), shortID(res.SessionID))
		return msg
	}
}

// moving attaches fold mode's cut to a fold; outside fold mode (p) the fold
// is all there is.
func (u uiModel) moving(fold tea.Cmd, at *tree.Node) tea.Cmd {
	if u.folding == nil {
		return fold
	}
	return cutAfter(fold, u.a, u.st, *u.folding, at.SessionID)
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
		if live != nil {
			// The summary call takes minutes, and a pane may have opened on
			// the session meanwhile. Whatever was true at confirm time is
			// checked again right before the transcript is read.
			_, status, err := live(op.src.ID)
			if err != nil {
				if op.summarise {
					return actionDoneMsg{status: "summary stored — cannot tell whether the session is busy: " + scrubbed(err, op.edit.Seed) + "; nothing was spliced"}
				}
				return actionDoneMsg{status: "cannot tell whether the session is busy: " + scrubbed(err, op.edit.Seed) + " — nothing was written"}
			}
			if busy(status) {
				if op.summarise {
					return actionDoneMsg{status: "summary stored — agent is " + status + "; select again or use p"}
				}
				return actionDoneMsg{status: "agent is " + status + " — wait for it to finish; nothing was written"}
			}
		}
		res, failed := writeEdit(a, st, op)
		if failed != "" {
			return actionDoneMsg{status: failed}
		}
		what := op.kind
		if op.kind == store.KindCut {
			what = fmt.Sprintf("cut %d turns from", res.Removed)
		}
		return actionDoneMsg{status: what + " " + shortID(op.src.ID) + " → " + shortID(res.SessionID) + " — ⏎ on it to continue there",
			reload: true, tip: res.SessionID}
	}
}

// writeEdit splices op and records the new line as replacing its source
// (§5.1). failed is the status when either did not happen.
func writeEdit(a adapter.Adapter, st *store.Store, op editOp) (adapter.Spliced, string) {
	res, err := a.Splice(op.src, op.edit, op.dst)
	if err != nil {
		return res, op.kind + " failed: " + scrubbed(err, op.edit.Seed)
	}
	b := store.Branch{Kind: op.kind, Title: op.title, CreatedAt: time.Now().UTC()}
	if op.kind == store.KindCut {
		b.Cut = &store.Cut{Turns: res.Removed, At: res.After}
	}
	st.Replace(op.src.ID, res.SessionID, b)
	if err := st.Save(); err != nil {
		return res, op.kind + " into " + shortID(res.SessionID) + ", but the tree was not saved: " + err.Error()
	}
	return res, ""
}

// handoverCmd opens a replacement with focus and only then closes the pane
// still running the line it replaced (§6.2): if the open fails, the old pane
// is all the user has, so it stays. The confirmation may have sat on screen a
// while, so the old pane is checked again before it is closed.
func handoverCmd(a adapter.Adapter, sid, dst, old, oldPane string, live LiveFunc, closePane ClosePaneFunc) tea.Cmd {
	return func() tea.Msg {
		if err := a.Resume(sid, dst, true); err != nil {
			return actionDoneMsg{status: "could not open " + shortID(sid) + " — old pane left running: " + err.Error()}
		}
		pane, status, err := live(old)
		if err != nil {
			return actionDoneMsg{status: "opened " + shortID(sid) + " — old pane left running: " + err.Error()}
		}
		if pane != oldPane {
			return actionDoneMsg{status: "opened " + shortID(sid) + " — the old pane is already gone", quit: true}
		}
		if busy(status) {
			return actionDoneMsg{status: "opened " + shortID(sid) + " — old pane left running: its agent is " + status}
		}
		if err := closePane(oldPane); err != nil {
			return actionDoneMsg{status: "opened " + shortID(sid) + ", but the old pane did not close: " + err.Error()}
		}
		return actionDoneMsg{status: "opened " + shortID(sid) + ", old pane closed", quit: true}
	}
}

// openTip is ⏎ on a line's tip. A tip already open in a pane is left alone:
// a second claude on one transcript would interleave both. If a line it
// replaced is still open in a pane, that pane is handed over after a
// confirmation; otherwise it is a plain resume, with nothing to confirm.
func (u uiModel) openTip(n *tree.Node) (tea.Model, tea.Cmd) {
	if u.live != nil {
		pane, _, err := u.live(n.SessionID)
		if err != nil {
			u.status = "cannot tell whether this session is open: " + err.Error()
			return u, nil
		}
		if pane != "" {
			u.status = "already open in pane " + pane
			return u, nil
		}
	}
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
			if busy(status) {
				u.status = "the old line's agent is " + status + " — wait for it to finish"
				return u, nil
			}
			u.confirm = fmt.Sprintf("Continue on the new line:  %q\n\nThe pane running the old line is closed; text typed but not sent there is lost.\n\n[enter] continue   [esc] back", n.Node.Title)
			u.pending, u.pendingBusy = handoverCmd(u.a, n.SessionID, u.dstCWD(n), old, pane, u.live, u.closePane), "opening session…"
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
	// Fold writes to this line only once the summary is placed, and its agent
	// is checked then (§2.7).
	if kind != kindFold && !u.liveCheck(src.ID) {
		return u, nil
	}
	sp, err := u.a.Widen(src, from.Node.ID, to.Node.ID)
	if err != nil {
		u.status = "cannot edit this range: " + err.Error()
		return u, nil
	}
	op := editOp{src: src, edit: adapter.Edit{From: from.Node.ID, To: to.Node.ID}, kind: kind,
		from: from, to: to, dst: u.dstCWD(to), title: "✂ cut"}
	if kind == store.KindCut {
		text := fmt.Sprintf("Cut turns %d–%d:\n\n  from  %q\n  to    %q\n\nRemoves turns %d–%d. Costs nothing. No note is left in the conversation.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, sp.First, sp.Last, replacesLine)
		u.confirm = text + "\n\n[enter] go   [esc] back"
		u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "cutting…"
		return u, nil
	}
	if kind == kindFold && sp.First <= 1 && sp.Last >= sp.Turns {
		u.status = "a fold must leave something behind — use p or branch here to copy a whole line"
		return u, nil
	}
	turns, entries, size, err := u.a.Preview(src, sp.End)
	if err != nil {
		u.status = "cannot summarise this range: " + err.Error()
		return u, nil
	}
	heading, then := "continue", fmt.Sprintf("turns %d–%d are replaced by the summary.\n%s", sp.First, sp.Last, replacesLine)
	if kind == kindFold {
		heading, then = "fold", fmt.Sprintf("you choose where to fold it in. When you do, turns %d–%d are cut from this line.", sp.First, sp.Last)
	}
	text := fmt.Sprintf("Summarise & %s turns %d–%d:\n\n  from  %q\n  to    %q\n\nThe model reads this session up to the end of the range — %d turn(s) · %d entries · %s — and describes only the range. That whole prefix is billed.\n\nThen: %s",
		heading, sp.First, sp.Last, from.Node.Title, to.Node.Title, turns, entries, humanBytes(size), then)
	u.confirm = text + "\n\n[enter] go   [esc] back"
	op.summarise = true
	u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "summarising…"
	if kind == kindFold {
		cut := op
		cut.kind, cut.summarise = store.KindCut, false
		u.pending = foldCmd(u.a, u.st, op, foldMove{cut: cut, first: sp.First, last: sp.Last})
	}
	return u, nil
}

// liveCheck reports whether sessionID may be edited. false means the edit is
// refused and u.status says why: a busy agent, or a herdr that will not say —
// an edit must know whether a turn is running under it, not guess.
func (u *uiModel) liveCheck(sessionID string) bool {
	if u.live == nil {
		return true
	}
	_, status, err := u.live(sessionID)
	if err != nil {
		u.status = "cannot tell whether this session is open: " + err.Error()
		return false
	}
	if busy(status) {
		u.status = "agent is " + status + " — wait for it to finish"
		return false
	}
	return true
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

// placeInFoldMode is ⏎ in fold mode: the move is refused before anything is
// written if it would fold the line into itself or the source's agent may be
// mid-turn (§2.7). A refusal stays in fold mode.
func (u uiModel) placeInFoldMode(at *tree.Node) (tea.Model, tea.Cmd) {
	src := u.folding.cut.src.ID
	if at.SessionID == src {
		u.status = "fold into another line — use continue for this one"
		return u, nil
	}
	if u.live != nil {
		_, status, err := u.live(src)
		if err != nil {
			u.status = "cannot tell whether the source is busy: " + err.Error()
			return u, nil
		}
		if busy(status) {
			u.status = "the source's agent is " + status + " — wait for it to finish"
			return u, nil
		}
	}
	return u.foldAt(at, u.folding.sum)
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
		u.confirm = foldBackConfirmText(at, turns, entries, size, u.folding.note())
		u.pending = u.moving(foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, u.agentFor(at), u.send), at)
		u.pendingBusy = "folding back…"
		return u, nil
	}
	if !u.liveCheck(at.SessionID) {
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
		dst: u.dstCWD(at), title: "⤶ " + title(sum.Text, 40)}
	u.confirm = fmt.Sprintf("Insert the summary after turn %d:  %q\n\nEverything after it is kept. Costs nothing.\n%s%s\n\n[enter] insert   [esc] back", sp.Last, at.Node.Title, replacesLine, u.folding.note())
	u.pending, u.pendingBusy = u.moving(editCmd(u.a, u.st, op, u.live), at), "inserting…"
	return u, nil
}

// foldAt is choosing sum for turn at, from p's picker or from fold mode: at
// the live tip it is the next message, anywhere else the place menu (§2.5).
// In fold mode each of those landings then cuts the source (§2.7).
func (u uiModel) foldAt(at *tree.Node, sum store.Summary) (tea.Model, tea.Cmd) {
	if agent := u.agentFor(at); agent != "" && at.IsSessionLeaf && u.send != nil {
		// Nothing is copied and nothing is written: the summary is the next
		// message. There is no cost to show.
		cmd := u.moving(foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, agent, u.send), at)
		u.busy, u.folding = "sending…", nil
		return u, cmd
	}
	u.placing, u.pickAt = sum, at
	u.menu, u.menuIdx = "place", 0
	return u, nil
}
