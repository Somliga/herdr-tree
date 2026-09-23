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
	pane      string     // pane holding src at confirm time; "" if none
	dst       string
}

// editCmd runs an edit to completion or to its first failure, in the order
// the spec fixes (§6.1). Each step runs only if the one before succeeded, and
// every status says what DID happen.
func editCmd(a adapter.Adapter, st *store.Store, op editOp, live LiveFunc, closePane ClosePaneFunc) tea.Cmd {
	return func() tea.Msg {
		if op.summarise {
			text, err := a.Summarise(op.src, op.from.Node.ID, op.to.Node.ID)
			if err != nil {
				return actionDoneMsg{status: "summarise failed: " + err.Error() + " — nothing was written"}
			}
			sum := store.Summary{Text: text, SessionID: op.src.ID,
				FromTurn: op.from.Node.ID, ToTurn: op.to.Node.ID, CreatedAt: time.Now().UTC()}
			st.AddSummary(sum)
			if err := st.Save(); err != nil {
				return actionDoneMsg{status: "summarised, but not saved: " + err.Error() + " — nothing was written"}
			}
			op.edit.Seed = foldBackSeed(op.from, sum, true)
			op.title = "⤶ " + title(text, 40)
		}
		if op.pane != "" {
			// The summary call takes minutes. Whatever was true at confirm
			// time is checked again right before the transcript is read.
			pane, status, err := live(op.src.ID)
			if err != nil {
				return actionDoneMsg{status: "cannot tell whether the session is busy: " + scrubbed(err, op.edit.Seed) + " — nothing was written"}
			}
			if status == "working" {
				if op.summarise {
					return actionDoneMsg{status: "summary stored — agent is busy; select again or use p"}
				}
				return actionDoneMsg{status: "agent is working — wait for it to finish; nothing was written"}
			}
			op.pane = pane
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
		if op.pane == "" {
			return actionDoneMsg{status: op.kind + " " + shortID(op.src.ID) + " → " + shortID(res.SessionID), reload: true,
				from: op.src.ID, to: res.SessionID}
		}
		if err := a.Resume(res.SessionID, op.dst, true); err != nil {
			return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + ", but it did not open — old pane left running: " + err.Error(),
				reload: true, from: op.src.ID, to: res.SessionID}
		}
		if err := closePane(op.pane); err != nil {
			return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + " and opened, but the old pane did not close: " + err.Error()}
		}
		return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + " — opened in a new pane, old pane closed", quit: true}
	}
}

const replacesLine = "A new session replaces this line in the tree (the old one is hidden, kept on disk)."
const liveLine = "The pane running this session is closed and the new one opens. Text typed but not sent in that pane is lost."

// editConfirm raises the one confirmation for a range edit (§2.3). kind is
// store.KindCompacted or store.KindCut.
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
	pane, ok := u.liveCheck(src.ID)
	if !ok {
		return u, nil
	}
	sp, err := u.a.Widen(src, from.Node.ID, to.Node.ID)
	if err != nil {
		u.status = "cannot edit this range: " + err.Error()
		return u, nil
	}
	op := editOp{src: src, edit: adapter.Edit{From: from.Node.ID, To: to.Node.ID}, kind: kind,
		from: from, to: to, pane: pane, dst: u.dstCWD(to), title: "✂ " + from.Node.Title}
	var text string
	if kind == store.KindCompacted {
		turns, entries, size, err := u.a.Preview(src, sp.End)
		if err != nil {
			u.status = "cannot summarise this range: " + err.Error()
			return u, nil
		}
		op.summarise = true
		text = fmt.Sprintf("Summarise & compact turns %d–%d:\n\n  from  %q\n  to    %q\n\nThe model reads this session up to the end of the range — %d turn(s) · %d entries · %s — and describes only the range. That whole prefix is billed.\n\nThen: turns %d–%d are replaced by the summary.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, turns, entries, humanBytes(size), sp.First, sp.Last, replacesLine)
		u.pendingBusy = "summarising…"
	} else {
		text = fmt.Sprintf("Cut turns %d–%d:\n\n  from  %q\n  to    %q\n\nRemoves turns %d–%d. Costs nothing. No note is left in the conversation.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, sp.First, sp.Last, replacesLine)
		u.pendingBusy = "cutting…"
	}
	if pane != "" {
		text += "\n" + liveLine
	}
	u.confirm = text + "\n\n[enter] go   [esc] back"
	u.pending = editCmd(u.a, u.st, op, u.live, u.closePane)
	return u, nil
}

// liveCheck resolves which pane holds sessionID. ok=false means the edit is
// refused and u.status says why: a working agent, or a herdr that will not
// say — an edit that might close a pane must know, not guess.
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

var rangeMenu = []string{"summarise & compact", "cut"}
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

// placeChosen is a stub: Task 7 wires the fold-back menu (§2.5) to it.
func (u uiModel) placeChosen(int) (tea.Model, tea.Cmd) { return u, nil }
