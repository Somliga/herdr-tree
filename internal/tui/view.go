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

// claudeSummaryPrefix and claudeCompactionPrefix mirror claude.SummaryPrefix
// and claude.CompactionPrefix. internal/tui must not import internal/claude,
// so the markers are duplicated deliberately — they are three words and the
// package boundary is worth more. A test in cmd/herdr-tree, which imports
// both, asserts the two pairs agree.
const (
	claudeSummaryPrefix    = "⤶ summary of"
	claudeCompactionPrefix = "⤶ compacted"
)

// SummaryPrefix and CompactionPrefix expose those copies to cmd/herdr-tree,
// the one package that imports both this and internal/claude, so a test there
// can assert they still agree. They are aliases rather than the definitions
// because the markers belong to the transcript format, not to the view.
const (
	SummaryPrefix    = claudeSummaryPrefix
	CompactionPrefix = claudeCompactionPrefix
)

// title reduces text to one line of at most max runes. A summary is several
// paragraphs; anything that becomes a row label has to be one line or it
// breaks the tree it is drawn in.
func title(text string, max int) string {
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			line = strings.TrimSpace(l)
			break
		}
	}
	r := []rune(line)
	if len(r) <= max {
		return line
	}
	if max < 1 {
		return ""
	}
	return string(r[:max-1]) + "…"
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
	if r.Node.FromRemoved {
		b.WriteString("from a removed stretch  ")
	}
	if r.Node.CutHere > 0 {
		b.WriteString(fmt.Sprintf("✂ %d turns cut  ", r.Node.CutHere))
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
	if r.Node.CutAfter > 0 {
		b.WriteString(fmt.Sprintf("   ✂ %d turns cut after this", r.Node.CutAfter))
	}

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
	// it takes the colour slot from rows whose colour is only decorative.
	// It does NOT take it from a row whose colour is carrying something:
	// Broken is data integrity, and the two summary colours are the only
	// thing separating "knowledge arrived" from "this line contracted" at a
	// glance. Those rows stay themselves; the ┃ still marks them as ranged,
	// which is precisely why §6b insists the glyph exists.
	switch {
	case !r.InRange:
	case key == StyleBroken, key == StyleImport, key == StyleCompaction:
	default:
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

// foldBackConfirmText is confirmText's sibling for a fold-back: the same
// graft, plus one injected turn, so the same figures.
func foldBackConfirmText(at *tree.Node, turns, entries int, size int64, dstCWD string) string {
	return fmt.Sprintf(
		"Fold the summary in at:  %q\n\nThis starts a NEW session carrying %d turn(s) · %d entries · %s, with the summary appended as its next turn.\nThe original is untouched.\n\nOpens: split right, unfocused in %s\n\n[enter] fold back   [esc] cancel",
		at.Node.Title, turns, entries, humanBytes(size), dstCWD)
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
	// abandoning is set by the first ctrl+c during a call, so the second one
	// is a deliberate choice rather than a reflex.
	abandoning bool
	quitting bool

	// send delivers text to a live agent, and liveAgent names the agent
	// running u.current. Both are injected by Run so this package keeps its
	// boundary — and so the tip-append path, the riskiest assumption in v2,
	// is testable without a running Herdr.
	send      SendFunc
	liveAgent string

	// pending is what the confirmation dialog will run if it is accepted,
	// captured when the dialog is raised rather than recomputed on enter.
	pending     tea.Cmd
	pendingBusy string

	// picking is the fold-back picker: the summaries on offer, the cursor
	// within them, and the turn the chosen one lands on.
	picking []store.Summary
	pickIdx int
	pickAt  *tree.Node

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
	rangeEnd := u.m.RangeEnd
	u.m = New(roots)
	u.m.SetTrunk(tree.Trunk(u.roots, u.current))
	// tree.Build runs once, in Run, so a scope toggle re-roots the SAME
	// nodes — the range's end is still a live pointer and there is no reason
	// to throw the user's in-progress selection away. If the new scope does
	// not contain it, RangeSpan reports ok=false on its own.
	u.m.RangeEnd = rangeEnd
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

// summariseConfirmText reports what summarising this range will cost.
//
// The counts are Preview's at the range's END, not the range's own: a summary
// is produced by grafting up to the end of the range and asking the model to
// describe the tail of it, so the model reads the whole conversation up to
// there. On a long trunk that prefix is the expensive part, and §4 is explicit
// that the confirmation must say so rather than quote the range alone.
func summariseConfirmText(from, to *tree.Node, rows, turns, entries int, size int64) string {
	return fmt.Sprintf(
		"Summarise %d row(s):\n\n  from  %q\n  to    %q\n\nThe model reads this session up to the end of the range — %d turn(s) · %d entries · %s — and describes only the range. That whole prefix is billed.\n\nNothing is written to the session; the summary is stored in the tree.\n\n[enter] summarise   [esc] cancel",
		rows, from.Node.Title, to.Node.Title, turns, entries, humanBytes(size))
}

// SendFunc delivers text to a live agent. It is injected rather than called
// directly so internal/tui keeps its package boundary — and so the tip-append
// path, which is the riskiest assumption in v2, is testable without a running
// Herdr. cmd/herdr-tree wires it to herdr.AgentPrompt.
type SendFunc func(agent, text string) error

func summariseCmd(a adapter.Adapter, st *store.Store, from, to *tree.Node) tea.Cmd {
	return func() tea.Msg {
		src := adapter.Session{ID: from.SessionID, CWD: from.SessionCWD, Path: from.SessionPath}
		text, err := a.Summarise(src, from.Node.ID, to.Node.ID)
		if err != nil {
			return actionDoneMsg{status: "summarise failed: " + err.Error()}
		}
		st.AddSummary(store.Summary{
			Text: text, SessionID: from.SessionID,
			FromTurn: from.Node.ID, ToTurn: to.Node.ID,
			CreatedAt: time.Now().UTC(),
		})
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: "summarised, but not saved: " + err.Error()}
		}
		// Deliberately does not quit: a summary is generated so it can be
		// folded back, and the user is one `p` away from doing that.
		return actionDoneMsg{status: "summarised " + shortID(from.SessionID) + " — press p on a turn to fold it back"}
	}
}

// foldBackSeed composes the injected turn's text.
//
// A summary of a DIFFERENT session arriving here is an import: knowledge came
// in from a line that was abandoned. A summary of THIS session's own turns is
// a compaction: the line contracted and nothing new arrived. The two are the
// same operation and the same machinery — only this prefix tells them apart,
// and the classifier and the palette read nothing else.
// foldBackSeed marks the entry by what the fold-back DOES, not by where the
// summary came from. §6b's two meanings are effects: blue says these turns
// were on this line and got replaced by something shorter, orange says
// knowledge arrived from a line that was abandoned.
//
// So rewinding matters. Folding a summary of this session's own turns 5..12
// onto its LIVE tip replaces nothing — all the turns are still ahead of it —
// and calling that a compaction renders blue over a line that did not
// contract. Only the graft path rewinds, so only the graft path may say
// compacted. Session identity alone cannot tell the two apart.
func foldBackSeed(at *tree.Node, sum store.Summary, rewinding bool) string {
	if rewinding && sum.SessionID == at.SessionID {
		return claudeCompactionPrefix + " " + shortID(sum.FromTurn) + ".." + shortID(sum.ToTurn) + "\n\n" + sum.Text
	}
	return claudeSummaryPrefix + " " + shortID(sum.SessionID) + "\n\n" + sum.Text
}

// scrubbed renders an error for the status line with the seed taken out of
// it.
//
// The status is the one line of this program that message content may never
// reach, and the cause comes from outside: herdr reports its own stderr, and
// herdr may quote back the prompt it rejected. Trusting it not to is the kind
// of assumption that holds until the day it does not, so every line of the
// seed is removed from the cause instead.
func scrubbed(err error, seed string) string {
	cause := err.Error()
	for _, line := range strings.Split(seed, "\n") {
		// Short lines are dropped: a blank line or a bare "⤶ compacted t1..t2"
		// matches too much of ordinary prose to be worth cutting.
		if line = strings.TrimSpace(line); len(line) >= 12 {
			cause = strings.ReplaceAll(cause, line, "…")
		}
	}
	return cause
}

// foldBackCmd appends a summary at a chosen turn.
//
// At the live session's tip the summary is simply the next message, and Herdr
// can deliver it: no graft, no copy, no new session. Anywhere else the
// timeline is changing shape, so it is a rewind seeded with the summary.
//
// A failed send does NOT fall back to grafting. The user asked to continue a
// conversation; handing them a fork instead gives them two lines where they
// expected one, and they will not notice until much later.
func foldBackCmd(a adapter.Adapter, st *store.Store, at *tree.Node, dst string, sum store.Summary, liveAgent string, send SendFunc) tea.Cmd {
	return func() tea.Msg {
		sending := at.IsSessionLeaf && liveAgent != "" && send != nil
		seed := foldBackSeed(at, sum, !sending)
		if sending {
			if err := send(liveAgent, seed); err != nil {
				return actionDoneMsg{status: "not sent to " + liveAgent + ": " + scrubbed(err, seed) + " — nothing was written"}
			}
			return actionDoneMsg{status: "sent to " + liveAgent, quit: true}
		}
		src := adapter.Session{ID: at.SessionID, CWD: at.SessionCWD, Path: at.SessionPath}
		sid, err := a.BranchSeeded(src, at.Node.ID, dst, seed)
		if err != nil {
			return actionDoneMsg{status: "fold back failed: " + scrubbed(err, seed)}
		}
		// Record the edge before resuming: the transcript now exists, so the
		// branch must survive even if opening it fails.
		st.Add(sid, store.Branch{
			GraftedFrom: store.From{SessionID: at.SessionID, Node: at.Node.ID},
			Title:       "⤶ " + title(sum.Text, 40),
			CreatedAt:   time.Now().UTC(),
		})
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: "folded " + shortID(sid) + ", but the tree was not saved: " + err.Error()}
		}
		if err := a.Resume(sid, dst); err != nil {
			return actionDoneMsg{status: "folded " + shortID(sid) + ", but it did not open: " + err.Error()}
		}
		return actionDoneMsg{status: "folded into " + shortID(sid), quit: true}
	}
}

// agentFor is the target to send to when appending at n, and "" when there is
// none.
//
// The two halves answer different questions. u.liveAgent, resolved by
// cmd/herdr-tree against herdr's live agent list, answers "is anything
// holding the session the user is in, and where". This guard answers "is n
// part of THAT session": other sessions in the forest are often live in other
// panes, and appending at one of their turns must not be delivered into
// whichever conversation the user happens to be sitting in.
func (u uiModel) agentFor(n *tree.Node) string {
	if n.SessionID != "" && n.SessionID == u.current {
		return u.liveAgent
	}
	return ""
}

// summariseConfirm raises the cost dialog for the range in progress.
func (u uiModel) summariseConfirm() (tea.Model, tea.Cmd) {
	from, to, ok := u.m.RangeSpan()
	if !ok {
		u.m.CancelRange()
		u.status = "the range is no longer on screen"
		return u, nil
	}
	if from.SessionID != to.SessionID {
		// Summarise grafts one transcript at the range's end and names the
		// start turn in it; a start that lives in another session is not in
		// that transcript at all. The range stays put so it can be adjusted.
		u.status = "a range must stay inside one session"
		return u, nil
	}
	rows := 0
	for _, r := range u.m.Rows() {
		if r.InRange {
			rows++
		}
	}
	src := adapter.Session{ID: to.SessionID, CWD: to.SessionCWD, Path: to.SessionPath}
	turns, entries, size, err := u.a.Preview(src, to.Node.ID)
	if err != nil {
		u.status = "cannot summarise this range: " + err.Error()
		return u, nil
	}
	u.confirm = summariseConfirmText(from, to, rows, turns, entries, size)
	u.pending, u.pendingBusy = summariseCmd(u.a, u.st, from, to), "summarising…"
	return u, nil
}

func (u uiModel) Init() tea.Cmd { return nil }

func (u uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
	case actionDoneMsg:
		u.busy = ""
		u.abandoning = false
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
			//
			// The first ctrl+c does not quit. Quitting exits the process, and
			// the watchdog that would kill the model call dies with it — the
			// call runs to completion and is billed either way. Leaving
			// silently makes that spend invisible, so say it once and let a
			// second press through for anyone who wants out regardless.
			if msg.String() == "ctrl+c" {
				if !u.abandoning {
					u.abandoning = true
					return u, nil
				}
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
		if u.picking != nil {
			switch msg.String() {
			case "up", "k":
				if u.pickIdx > 0 {
					u.pickIdx--
				}
			case "down", "j":
				if u.pickIdx < len(u.picking)-1 {
					u.pickIdx++
				}
			case "enter":
				sum, at := u.picking[u.pickIdx], u.pickAt
				u.picking, u.pickAt, u.pickIdx = nil, nil, 0
				agent := u.agentFor(at)
				if agent != "" && at.IsSessionLeaf && u.send != nil {
					// Nothing is copied and nothing is written: the summary
					// is the next message. There is no cost to show.
					u.busy = "sending…"
					return u, foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, agent, u.send)
				}
				// The other path is the same graft ⏎ on a turn confirms,
				// plus a pane open, and on a long transcript it is the more
				// expensive of the two. It gets the same figures first.
				src := adapter.Session{ID: at.SessionID, CWD: at.SessionCWD, Path: at.SessionPath}
				turns, entries, size, err := u.a.Preview(src, at.Node.ID)
				if err != nil {
					u.status = "cannot fold back here: " + err.Error()
					return u, nil
				}
				u.confirm = foldBackConfirmText(at, turns, entries, size, u.dstCWD(at))
				u.pending = foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, agent, u.send)
				u.pendingBusy = "folding back…"
				return u, nil
			case "esc", "q":
				u.picking, u.pickAt, u.pickIdx = nil, nil, 0
			}
			return u, nil
		}
		if u.confirm != "" {
			switch msg.String() {
			case "enter":
				cmd, busy := u.pending, u.pendingBusy
				u.confirm, u.pending, u.pendingBusy = "", nil, ""
				if cmd == nil {
					return u, nil
				}
				u.m.CancelRange() // acted on; a summarise consumes its range
				u.busy = busy
				return u, cmd
			case "esc", "q":
				// The range survives: escaping the cost dialog is how you go
				// back and move the range's start, not how you abandon it.
				u.confirm, u.pending, u.pendingBusy = "", nil, ""
			}
			return u, nil
		}
		switch msg.String() {
		case "esc":
			// A range in progress is what esc abandons. Quitting here would
			// take the overlay down with it, which is not what "never mind"
			// means when you are halfway through selecting something.
			if u.m.RangeEnd != nil {
				u.m.CancelRange()
				u.status = "range cancelled"
				return u, nil
			}
			u.quitting = true
			return u, tea.Quit
		case "q", "ctrl+c":
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
		case "s":
			if u.m.RangeEnd == nil {
				if u.m.Selected() == nil {
					return u, nil // nothing to range over; say nothing
				}
				// The END first: "summarise what I just did" is how the
				// thought arrives, and the cursor is already there.
				u.m.BeginRange()
				u.status = "range end fixed — move to its start, then s or ⏎ (esc cancels)"
				return u, nil
			}
			return u.summariseConfirm()
		case "p":
			n := u.m.Selected()
			if n == nil || n.Broken || n.Node.ID == "" {
				return u, nil
			}
			sums := u.st.AllSummaries()
			if len(sums) == 0 {
				// Offered, never forced: say where a summary comes from
				// rather than refusing the key.
				u.status = "no summaries yet — s summarises a range, then p folds it back in"
				return u, nil
			}
			u.picking, u.pickIdx, u.pickAt = sums, 0, n
		case "enter":
			if u.m.RangeEnd != nil {
				return u.summariseConfirm()
			}
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
			u.pending, u.pendingBusy = branchCmd(u.a, u.st, n, u.dstCWD(n)), "branching…"
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

// pickerView lists the summaries on offer and says, in words, which of the
// two mechanisms the chosen one will use. The distinction is not cosmetic —
// one continues the conversation the user is in, the other starts a second
// line — so it is stated before the key that commits to it, not after.
func (u uiModel) pickerView() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fold a summary in at:  %q\n\n", u.pickAt.Node.Title)
	for i, s := range u.picking {
		marker := "  "
		if i == u.pickIdx {
			marker = "> "
		}
		kind := "from " + shortID(s.SessionID)
		if s.SessionID == u.pickAt.SessionID {
			kind = "compacts " + shortID(s.FromTurn) + ".." + shortID(s.ToTurn)
		}
		fmt.Fprintf(&b, "%s%-28s %s\n", marker, kind, title(s.Text, 48))
	}
	b.WriteString("\n")
	if agent := u.agentFor(u.pickAt); agent != "" && u.pickAt.IsSessionLeaf && u.send != nil {
		fmt.Fprintf(&b, "Sends it to %s as your next message. Nothing is copied.\n", agent)
	} else {
		b.WriteString("Starts a NEW session that rewinds to this turn and carries the summary.\n")
	}
	b.WriteString("\n↑↓ choose   [enter] fold back   [esc] cancel\n")
	return b.String()
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
	if u.picking != nil {
		return u.pickerView()
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
	if u.m.RangeEnd != nil {
		// While a range is being selected, three keys change meaning. Saying
		// so is cheaper than the user discovering that esc no longer closes.
		b.WriteString("↑↓ move to the range's start  s/⏎ summarise  esc cancel range\n")
	} else {
		b.WriteString(fmt.Sprintf("↑↓ move  ←→ fold  ⏎ continue  s summarise  p fold back  L label  a scope:%s  f filter:%s  esc close\n", scope, u.m.Filter))
	}
	if u.busy != "" {
		b.WriteString(u.busy + "\n")
		if u.abandoning {
			b.WriteString("this call is already billed; ctrl+c again to leave it running\n")
		}
	}
	if u.status != "" {
		b.WriteString(u.status + "\n")
	}
	return b.String()
}

// Run starts the overlay. liveAgent is the Herdr agent running `current`, and
// send delivers text to it; both may be zero, in which case a fold-back at
// that session's tip grafts like any other turn and the picker says so.
func Run(a adapter.Adapter, repoRoot string, st *store.Store, sessions []adapter.Session, current, liveAgent string, send SendFunc) error {
	roots := tree.Build(sessions, st)
	u := uiModel{m: New(roots), a: a, st: st, repoRoot: repoRoot, current: current, roots: roots, liveAgent: liveAgent, send: send}
	u.rebuild() // start scoped to the current session
	_, err := tea.NewProgram(u, tea.WithAltScreen()).Run()
	return err
}
