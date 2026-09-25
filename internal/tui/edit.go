package tui

import (
	"errors"
	"fmt"
	"strings"
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

// foldMove is target mode's hand (§2.7): the range still to be summarised,
// and its drop from the source, both run only once the target is confirmed.
type foldMove struct {
	op          editOp        // the summary to make
	sum         store.Summary // set once made, so a failure can scrub it
	cut         editOp
	first, last int    // the widened range, for the texts
	cost        string // the cost text, up to "Then: "
}

// note is what the place menu and every confirmation on the move add.
func (mv *foldMove) note() string {
	if mv == nil {
		return ""
	}
	return fmt.Sprintf("\n…and turns %d–%d are dropped from %s", mv.first, mv.last, shortID(mv.cut.src.ID))
}

// changedElsewhere is §5.1's refusal: "" if no line in sids was replaced
// on disk since st loaded, else why the edit is refused. It is asked right
// before anything is paid for or written, and again before a splice that
// follows a summary. ponytail: a window is left between the check and the
// splice's Save, which Save's merge settles for the later edit.
func changedElsewhere(st *store.Store, sids ...string) string {
	for _, sid := range sids {
		replaced, err := st.ReplacedOnDisk(sid)
		if err != nil {
			return "cannot tell whether this line was changed in another overlay: " + err.Error() + " — nothing was written"
		}
		if replaced {
			return "this line was changed in another overlay — reopen the tree"
		}
	}
	return ""
}

// moveCmd is squash into…'s confirmed move (§2.7): the source is asked, the
// summary made and stored, land writes it at the target, and cutAfter drops
// the range from the source. Each runs only if the one before succeeded.
//
// merge says land splices target, so target is checked as well as the source.
// A branch or a send leaves target as it is and may start from an old line.
func moveCmd(a adapter.Adapter, st *store.Store, mv foldMove, target string, merge bool, live LiveFunc, land func(store.Summary) tea.Cmd) tea.Cmd {
	lines := []string{mv.cut.src.ID}
	if merge {
		lines = append(lines, target)
	}
	return func() tea.Msg {
		if stale := changedElsewhere(st, lines...); stale != "" {
			return actionDoneMsg{status: stale}
		}
		if live != nil {
			_, status, err := live(mv.cut.src.ID)
			if err != nil {
				return actionDoneMsg{status: "cannot tell whether the source is busy: " + err.Error() + " — nothing was paid or written"}
			}
			if busy(status) {
				return actionDoneMsg{status: "the source's agent is " + status + " — wait for it to finish; nothing was paid or written"}
			}
		}
		sum, failed := summariseRange(a, st, mv.op)
		if failed != "" {
			return actionDoneMsg{status: failed}
		}
		mv.sum = sum
		if stale := changedElsewhere(st, lines...); stale != "" {
			return actionDoneMsg{status: "summary stored — " + stale}
		}
		return cutAfter(land(sum), a, st, mv, target, live)()
	}
}

// cutAfter runs fold and, only if it landed, drops mv's range from its
// source. The fold comes first: if the drop then fails, the stretch is in two
// places, never in none. target is the session the user squashed into. The
// drop is editCmd's, so the source's agent is asked again right before it
// (§6.1).
func cutAfter(fold tea.Cmd, a adapter.Adapter, st *store.Store, mv foldMove, target string, live LiveFunc) tea.Cmd {
	return func() tea.Msg {
		msg := fold().(actionDoneMsg)
		// A fold reloads or quits only when it wrote and recorded; anything
		// else is its failure, and the source stays as it is. The summary was
		// paid for and stored, so p can place it without paying again.
		if !msg.reload && !msg.quit {
			msg.status = "summary stored — " + msg.status
			return msg
		}
		cut := editCmd(a, st, mv.cut, live)().(actionDoneMsg)
		if !cut.reload {
			tip := msg.tip
			if tip == "" {
				tip = target
			}
			return actionDoneMsg{status: "squashed into " + shortID(target) + ", but the source was not dropped: " + scrubbed(errors.New(cut.status), mv.sum.Text), reload: true, tip: tip}
		}
		// "dropped n turns from <src> → <new>", without the pointer to the
		// drop: the cursor lands on the fold's result.
		msg.status = "squashed into " + shortID(target) + ", " + strings.TrimSuffix(cut.status, continueThere)
		return msg
	}
}

// confirmMove raises target mode's one confirmation: the cost, then what
// lands at the target and what leaves the source (§2.7).
func (u uiModel) confirmMove(at *tree.Node, then string, merge bool, land func(store.Summary) tea.Cmd) (tea.Model, tea.Cmd) {
	mv := u.folding
	u.confirm = fmt.Sprintf("%s%s · turns %d–%d are dropped from %s\n\n[enter] go   [esc] back",
		mv.cost, then, mv.first, mv.last, shortID(mv.cut.src.ID))
	u.pending, u.pendingBusy = moveCmd(u.a, u.st, *mv, at.SessionID, merge, u.live, land), "summarising…"
	return u, nil
}

const continueThere = " — ⏎ on it to continue there"

// verb names op.kind for status text and confirmations, in git vocabulary:
// squash, drop, merge. The store kind itself (store.KindCompacted etc.) is
// unchanged — it is not shown, so only its display name moves.
func verb(kind string) string {
	switch kind {
	case store.KindCompacted:
		return "squashed"
	case store.KindCut:
		return "drop"
	case store.KindInserted:
		return "merged"
	}
	return kind
}

// editCmd runs an edit to completion or to its first failure, in the order
// the spec fixes (§6.1). Each step runs only if the one before succeeded, and
// every status says what DID happen. It only writes: moving to the new line
// is the user's own ⏎ (§6.2).
func editCmd(a adapter.Adapter, st *store.Store, op editOp, live LiveFunc) tea.Cmd {
	return func() tea.Msg {
		if stale := changedElsewhere(st, op.src.ID); stale != "" {
			return actionDoneMsg{status: stale}
		}
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
		if op.summarise {
			if stale := changedElsewhere(st, op.src.ID); stale != "" {
				return actionDoneMsg{status: "summary stored — " + stale}
			}
		}
		res, err := a.Splice(op.src, op.edit, op.dst)
		if err != nil {
			return actionDoneMsg{status: verb(op.kind) + " failed: " + scrubbed(err, op.edit.Seed)}
		}
		b := store.Branch{Kind: op.kind, Title: op.title, CreatedAt: time.Now().UTC()}
		if op.kind == store.KindCut {
			b.Cut = &store.Cut{Turns: res.Removed, At: res.After}
		}
		st.Replace(op.src.ID, res.SessionID, b)
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: verb(op.kind) + " into " + shortID(res.SessionID) + ", but the tree was not saved: " + err.Error()}
		}
		what := verb(op.kind)
		if op.kind == store.KindCut {
			what = fmt.Sprintf("dropped %d turns from", res.Removed)
		}
		return actionDoneMsg{status: what + " " + shortID(op.src.ID) + " → " + shortID(res.SessionID) + continueThere,
			reload: true, tip: res.SessionID}
	}
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
			u.confirm = fmt.Sprintf("Check out the new line:  %q\n\nThe pane running the old line is closed; text typed but not sent there is lost.\n\n[enter] continue   [esc] back", n.Node.Title)
			u.pending, u.pendingBusy = handoverCmd(u.a, n.SessionID, u.dstCWD(n), old, pane, u.live, u.closePane), "opening session…"
			return u, nil
		}
	}
	u.busy = "opening session…"
	return u, resumeCmd(u.a, n, u.dstCWD(n))
}

const replacesLine = "A new session replaces this line in the tree (the old one is hidden, kept on disk)."

// kindFold names squash into… in editConfirm. It is not a store kind: the
// move writes a merge or branch at its target and a drop (KindCut) here.
const kindFold = "fold"

// editConfirm raises the one confirmation for a range option (§2.3), or for
// squash into… enters target mode, whose confirmation follows the target. kind
// is store.KindCompacted (squash), kindFold (squash into…) or store.KindCut
// (drop).
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
	// squash into… writes to this line only once its target is confirmed, and
	// its agent is checked then (§2.7).
	if kind != kindFold && !u.liveCheck(src.ID) {
		return u, nil
	}
	sp, err := u.a.Widen(src, from.Node.ID, to.Node.ID)
	if err != nil {
		u.status = "cannot edit this range: " + err.Error()
		return u, nil
	}
	op := editOp{src: src, edit: adapter.Edit{From: from.Node.ID, To: to.Node.ID}, kind: kind,
		from: from, to: to, dst: u.dstCWD(to), title: "✂ drop"}
	if kind == store.KindCut {
		text := fmt.Sprintf("Drop turns %d–%d:\n\n  from  %q\n  to    %q\n\nRemoves turns %d–%d. Costs nothing. No note is left in the conversation.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, sp.First, sp.Last, replacesLine)
		u.confirm = text + "\n\n[enter] go   [esc] back"
		u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "dropping…"
		return u, nil
	}
	if kind == kindFold && sp.First <= 1 && sp.Last >= sp.Turns {
		u.status = "a squash into… must leave something behind — use p or branch here to copy a whole line"
		return u, nil
	}
	turns, entries, size, err := u.a.Preview(src, sp.End)
	if err != nil {
		u.status = "cannot summarise this range: " + err.Error()
		return u, nil
	}
	heading := "Squash"
	if kind == kindFold {
		heading = "Squash into…"
	}
	cost := fmt.Sprintf("%s turns %d–%d:\n\n  from  %q\n  to    %q\n\nThe model reads this session up to the end of the range — %d turn(s) · %d entries · %s — and describes only the range. That whole prefix is billed.\n\nThen: ",
		heading, sp.First, sp.Last, from.Node.Title, to.Node.Title, turns, entries, humanBytes(size))
	if kind == kindFold {
		// Target mode: nothing is paid or written until the target is
		// confirmed (§2.7).
		cut := op
		cut.kind = store.KindCut
		u.folding = &foldMove{op: op, cut: cut, first: sp.First, last: sp.Last, cost: cost}
		u.m.CancelRange()
		u.status = fmt.Sprintf("move to a turn and press ⏎ to squash turns %d–%d into it · esc cancels", sp.First, sp.Last)
		return u, nil
	}
	u.confirm = cost + fmt.Sprintf("turns %d–%d are replaced by the summary.\n%s", sp.First, sp.Last, replacesLine) + "\n\n[enter] go   [esc] back"
	op.summarise = true
	u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live), "summarising…"
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

var rangeMenu = []string{
	"squash — replace these turns with a summary",
	"squash into… — summarise, put it in another line, drop it here",
	"drop — remove these turns",
}
var placeMenu = []string{"merge here", "branch here"}

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

// placeInFoldMode is ⏎ in target mode: folding the line into itself is
// refused and stays in target mode (§2.7). The source's agent is asked only on
// confirm, by moveCmd.
func (u uiModel) placeInFoldMode(at *tree.Node) (tea.Model, tea.Cmd) {
	if at.SessionID == u.folding.cut.src.ID {
		u.status = "merge into another line — use squash for this one"
		return u, nil
	}
	return u.foldAt(at, store.Summary{})
}

// placeChosen acts on the placement menu. Merge rewrites the line in place
// and hides the old one; branch is v2's seeded graft and leaves both visible.
// In target mode the summary does not exist yet, so each is raised as land,
// run by the move once the summary is made.
func (u uiModel) placeChosen(idx int) (tea.Model, tea.Cmd) {
	at, sum := u.pickAt, u.placing
	u.pickAt = nil
	src := adapter.Session{ID: at.SessionID, CWD: at.SessionCWD, Path: at.SessionPath}
	if idx == 1 {
		// branch here (§2.5b): graft after at's whole turn, not at's own
		// entry. When at is already the turn's last entry (the common case
		// for a live tip), Widen is a no-op.
		sp, err := u.a.Widen(src, at.Node.ID, at.Node.ID)
		if err != nil {
			u.status = "cannot branch here: " + err.Error()
			return u, nil
		}
		land := func(sum store.Summary) tea.Cmd {
			return foldBackCmd(u.a, u.st, at, sp, u.dstCWD(at), sum, u.agentFor(at), u.send)
		}
		if u.folding != nil {
			return u.confirmMove(at, "a new line branches at "+shortID(at.SessionID)+", carrying the summary", false, land)
		}
		turns, entries, size, err := u.a.Preview(src, sp.End)
		if err != nil {
			u.status = "cannot branch here: " + err.Error()
			return u, nil
		}
		u.confirm = foldBackConfirmText(at, turns, entries, size)
		u.pending, u.pendingBusy = land(sum), "branching…"
		return u, nil
	}
	if !u.liveCheck(at.SessionID) {
		return u, nil
	}
	land := func(sum store.Summary) tea.Cmd {
		// Nothing is removed, so nothing contracted: a merge is always marked
		// as knowledge arriving, whatever session the summary came from.
		op := editOp{src: src, edit: adapter.Edit{After: at.Node.ID, Seed: foldBackSeed(at, sum, false)}, kind: store.KindInserted,
			dst: u.dstCWD(at), title: "⤶ " + title(sum.Text, 40)}
		return editCmd(u.a, u.st, op, u.live)
	}
	if u.folding != nil {
		return u.confirmMove(at, "the summary is merged into "+shortID(at.SessionID), true, land)
	}
	sp, err := u.a.Widen(src, at.Node.ID, at.Node.ID)
	if err != nil {
		u.status = "cannot merge here: " + err.Error()
		return u, nil
	}
	u.confirm = fmt.Sprintf("Merge the summary after turn %d:  %q\n\nEverything after it is kept. Costs nothing.\n%s\n\n[enter] merge   [esc] back", sp.Last, at.Node.Title, replacesLine)
	u.pending, u.pendingBusy = land(sum), "merging…"
	return u, nil
}

// foldAt is choosing sum for turn at, from p's picker or from target mode: at
// the live tip it is the next message, anywhere else the place menu (§2.5).
// In target mode each of those landings is confirmed with the cost (§2.7).
func (u uiModel) foldAt(at *tree.Node, sum store.Summary) (tea.Model, tea.Cmd) {
	if agent := u.agentFor(at); agent != "" && at.IsSessionLeaf && u.send != nil {
		// The live tip: foldBackCmd's send path ignores sp entirely.
		land := func(sum store.Summary) tea.Cmd {
			return foldBackCmd(u.a, u.st, at, entrySpan(at.Node.ID), u.dstCWD(at), sum, agent, u.send)
		}
		if u.folding != nil {
			return u.confirmMove(at, "the summary is sent to "+agent+" as your next message", false, land)
		}
		// Nothing is copied and nothing is written: the summary is the next
		// message. There is no cost to show.
		u.busy = "sending…"
		return u, land(sum)
	}
	u.placing, u.pickAt = sum, at
	u.menu, u.menuIdx = "place", 0
	return u, nil
}
