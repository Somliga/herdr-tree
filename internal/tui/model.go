// Package tui renders the conversation forest. The navigation model is kept
// separate from rendering so it can be tested as plain functions.
package tui

import (
	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

type Row struct {
	Node        *tree.Node
	Depth       int
	HasChildren bool
	Folded      bool
	// BodyCount is how many of this node's descendants belong to its own
	// section body — what a folded row is hiding.
	BodyCount int
	// OnTrunk is whether this row's session is on the current lineage. See
	// Model.SetTrunk.
	OnTrunk bool
	// InRange is whether this row falls within the current summarisation
	// range. See Model.BeginRange.
	InRange bool
}

// bodyCount counts a node's descendants that are its own section body: same
// session, not a further section head. A section's body entries have no
// same-session children of their own, so this only ever recurses one level
// deep in practice, but it is written to hold if that ever changes.
func bodyCount(n *tree.Node) int {
	count := 0
	for _, c := range n.Children {
		if c.SessionID != n.SessionID || c.IsHead {
			continue
		}
		count++
		count += bodyCount(c)
	}
	return count
}

// Filter is which kinds of entry are shown. Pi's lesson: filtering is a TREE
// operation, not row hiding — a hidden node's children re-parent onto its
// nearest visible ancestor, so the fork structure between surviving rows
// stays intact. Rows() already descends through hidden nodes at the parent's
// depth, which is that re-parenting.
type Filter int

const (
	FilterDefault Filter = iota // prompts, replies and tool calls
	FilterHuman                 // only what a person typed
)

func (f Filter) String() string {
	if f == FilterHuman {
		return "human"
	}
	return "all"
}

type Model struct {
	Roots   []*tree.Node
	Cursor  int
	Folded  map[*tree.Node]bool
	Filter  Filter
	OnTrunk map[string]bool
	// RangeEnd is the fixed end of a summarisation range, set by BeginRange.
	// nil means no range is in progress.
	RangeEnd *tree.Node

	parent map[*tree.Node]*tree.Node
}

// BeginRange fixes the END of a summarisation range at the cursor. The end
// first, because "summarise what I just did" is how the thought arrives and
// the cursor is already there — the reverse of most range pickers, and
// deliberately so (spec's flagged-as-worth-trying reversal).
func (m *Model) BeginRange() {
	m.RangeEnd = m.Selected()
}

// CancelRange clears an in-progress range without acting on it.
func (m *Model) CancelRange() { m.RangeEnd = nil }

// RangeSpan returns the range in document order regardless of which end the
// cursor is on, or ok=false when no range is active or either end has
// scrolled out of the current rows (e.g. the tree was rebuilt or refiltered
// out from under it).
func (m *Model) RangeSpan() (from, to *tree.Node, ok bool) {
	if m.RangeEnd == nil {
		return nil, nil, false
	}
	cur := m.Selected()
	if cur == nil {
		return nil, nil, false
	}
	rows := m.Rows()
	ci, ei := -1, -1
	for i, r := range rows {
		if r.Node == cur {
			ci = i
		}
		if r.Node == m.RangeEnd {
			ei = i
		}
	}
	if ci < 0 || ei < 0 {
		return nil, nil, false
	}
	if ci <= ei {
		return cur, m.RangeEnd, true
	}
	return m.RangeEnd, cur, true
}

// rangeIndices is RangeSpan's index-only sibling, used by Rows itself: it
// looks up the same span against the rows slice Rows already built, rather
// than calling Rows again and recursing.
func (m *Model) rangeIndices(rows []Row) (from, to int, ok bool) {
	if m.RangeEnd == nil || m.Cursor < 0 || m.Cursor >= len(rows) {
		return 0, 0, false
	}
	ei := -1
	for i, r := range rows {
		if r.Node == m.RangeEnd {
			ei = i
			break
		}
	}
	if ei < 0 {
		return 0, 0, false
	}
	if m.Cursor <= ei {
		return m.Cursor, ei, true
	}
	return ei, m.Cursor, true
}

// SetTrunk records which sessions are on the current lineage. An empty or nil
// set means there is no live session, and the tree falls back to v1's shape
// rather than guessing which line matters.
func (m *Model) SetTrunk(sessions map[string]bool) { m.OnTrunk = sessions }

func (m *Model) onTrunk(n *tree.Node) bool {
	return len(m.OnTrunk) > 0 && m.OnTrunk[n.SessionID]
}

func (m *Model) CycleFilter() {
	was := m.Selected()
	m.Filter = (m.Filter + 1) % 2
	if was == nil {
		return
	}
	for i, r := range m.Rows() {
		if r.Node == was {
			m.Cursor = i
			return
		}
	}
	m.clamp()
}

func (m *Model) shows(n *tree.Node) bool {
	if n.Superseded {
		return false // a branch's copy of a turn its parent already shows
	}
	if n.IsSessionRoot {
		return true
	}
	if m.Filter == FilterHuman {
		return n.Node.Kind == adapter.KindHuman
	}
	return true
}

// Window returns the slice of rows to draw for a viewport of the given
// height, the index it starts at, and the total. The selection is pinned
// near the middle once it has travelled that far, so holding an arrow
// scrolls the list rather than running the cursor off the edge — Pi computes
// its start index per render from the selection alone, with no stored scroll
// offset, and so does this.
func (m *Model) Window(height int) ([]Row, int, int) {
	rows := m.Rows()
	if height < 1 {
		height = 1
	}
	if len(rows) <= height {
		return rows, 0, len(rows)
	}
	half := height / 2
	start := m.Cursor - half
	if start < 0 {
		start = 0
	}
	if start > len(rows)-height {
		start = len(rows) - height
	}
	return rows[start : start+height], start, len(rows)
}

// New indexes each node's parent so Fold can jump upward.
//
// The "already seen" check is the same cycle defence as Rows(), and it is
// needed here for a harsher reason: an unguarded recursive walk over a cyclic
// tree overflows the stack, and a Go stack overflow is a fatal error that no
// recover can catch. That kills the plugin process outright rather than
// merely freezing the view. Graft edges live in a plain JSON file that can be
// hand-edited or corrupted into a cycle, so this is reachable.
// ScopeTo narrows the forest to sessionID's family (§5.3d): the top-level
// root whose tree contains it, with every branch anywhere in that tree —
// not just the session's own turns and what was grafted from it. A branch
// deep in a long conversation is buried inside its family's root, not a
// root of its own, so this walks each top-level root looking for sessionID
// rather than searching for a node that carries it directly.
//
// This is the default because it is the actual workflow — see the whole
// conversation you are in, including the point you branched from and
// anything else that branched from the same place, not just your own line
// forward. A repo-wide forest is the rarer case (looking back at an
// unrelated old conversation), and on a real repo it is dozens of sessions
// of somebody else's turns between you and the one you wanted. `a` still
// shows all sessions.
//
// Returns nil when the session is not in the forest, so the caller can fall
// back to showing everything rather than showing nothing.
func ScopeTo(roots []*tree.Node, sessionID string) []*tree.Node {
	if sessionID == "" {
		return nil
	}
	contains := func(root *tree.Node) bool {
		found := false
		seen := map[*tree.Node]bool{}
		var walk func(n *tree.Node)
		walk = func(n *tree.Node) {
			if found || seen[n] {
				return
			}
			seen[n] = true
			if n.IsSessionRoot && n.SessionID == sessionID {
				found = true
				return
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(root)
		return found
	}
	for _, r := range roots {
		if contains(r) {
			return []*tree.Node{r}
		}
	}
	return nil
}

func New(roots []*tree.Node) *Model {
	m := &Model{Roots: roots, Folded: map[*tree.Node]bool{}, parent: map[*tree.Node]*tree.Node{}}
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		for _, c := range n.Children {
			if _, seen := m.parent[c]; seen {
				continue
			}
			m.parent[c] = n
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	// Sections start folded. The point of opening the tree is to find a turn,
	// not to read 684 rows of tool calls; the body is one keypress away.
	//
	// A Superseded head is never given a row (shows() hides it), so nothing
	// can ever select it to unfold it — folding it here would bury its own
	// body (a branch's first turns of its own) forever. Rows() also treats
	// a Superseded node as unfolded regardless of this map, as a second line
	// of defence; this loop skipping it is what keeps Fold/Unfold from ever
	// having to think about it.
	for n := range m.parent {
		if n.IsHead && len(n.Children) > 0 && !n.Superseded {
			m.Folded[n] = true
		}
	}
	for _, r := range roots {
		if r.IsHead && len(r.Children) > 0 && !r.Superseded {
			m.Folded[r] = true
		}
	}
	return m
}

// orderedChildren returns n's children in render order — this turn's own
// body first, then every branch that left here (the one on the trunk, if
// any, sorted first among them), then the parent's own same-session
// continuation last — plus, for each child, whether it carries the bar: the
// trunk graft if there is one, else the same-session continuation, never
// both (§5.3d: a branch is always indented, so it no longer takes the
// continuation's place — only the bar still tells the trunk apart from a
// branch that happens to sit right beside it).
//
// Putting the divergence before the continuation is what makes a branch
// render immediately under the turn it left rather than after the whole
// remaining trunk — Build appends a graft as the LAST child of the node it
// left, so walking children in Build's own order drags the rest of the trunk
// between a divergence and its branch. This is the one place that ordering
// is decided, shared by both the folded and unfolded loops in Rows, so the
// rule is never duplicated.
//
// Only n's OWN call (n.IsHead) collects grafts, and it collects them from
// anywhere in n's own turn — n's direct cross-session children AND any
// hanging off one of n's body rows, via bodyGrafts. Task 20's whole-turn
// rule almost always grafts on a reply or a tool result, both body rows,
// never the head itself, so treating those as if they were n's own direct
// children is what keeps a branch at a single indent under its turn's head
// rather than one further indent under the body row it happened to land on
// — and what keeps it visible when that head is folded: Rows() only hides a
// folded head's own body ROWS, never what the head's own order returns. A
// body node's call returns none: the head above it already claimed them, so
// walking them again from the body row would give them the wrong (deeper)
// depth and, when unfolded, a duplicate.
func (m *Model) orderedChildren(n *tree.Node, nOnTrunk bool) (order []*tree.Node, onTrunkOf map[*tree.Node]bool) {
	// cont is always n's own chain continuing: the next section head in the
	// SAME session, always a direct child — Build only ever chains a
	// session's own turns directly onto its current head.
	var cont *tree.Node
	for _, c := range n.Children {
		if c.IsHead && c.SessionID == n.SessionID {
			cont = c
			break
		}
	}
	var body []*tree.Node
	for _, c := range n.Children {
		if c.SessionID == n.SessionID && !c.IsHead {
			body = append(body, c)
		}
	}
	var grafts []*tree.Node
	if n.IsHead {
		grafts = bodyGrafts(n)
	}
	var trunkGraft *tree.Node
	if nOnTrunk {
		for _, c := range grafts {
			if m.OnTrunk[c.SessionID] {
				trunkGraft = c
				break
			}
		}
	}

	onTrunkOf = map[*tree.Node]bool{}
	for _, c := range body {
		// This turn's own body: stays on-trunk exactly when n does.
		onTrunkOf[c] = nOnTrunk
	}
	order = append(order, body...)
	if trunkGraft != nil {
		order = append(order, trunkGraft)
		onTrunkOf[trunkGraft] = true
	}
	for _, c := range grafts {
		if c == trunkGraft {
			continue
		}
		order = append(order, c)
		onTrunkOf[c] = false
	}
	if cont != nil {
		// The bar goes to the trunk graft when there is one; the same-session
		// tail that continues past it is the abandoned one (§5.3d).
		onTrunkOf[cont] = nOnTrunk && trunkGraft == nil
		order = append(order, cont)
	}
	return order, onTrunkOf
}

// bodyGrafts collects every cross-session child anywhere within n's own
// turn's body, in the order Build attached them: n's own direct
// cross-session children, then any that landed on one of n's body rows
// instead — the common case since Task 20's whole-turn rule almost always
// grafts on a reply or a tool result. A body row has no same-session
// children of its own (Build only ever chains a session's own turns onto
// its current HEAD, never onto a body row), so this recursion only ever
// goes one level past n in practice — the same invariant bodyCount relies
// on — but is written to hold if that ever changes.
func bodyGrafts(n *tree.Node) []*tree.Node {
	var out []*tree.Node
	for _, c := range n.Children {
		if c.SessionID != n.SessionID {
			out = append(out, c)
			continue
		}
		if !c.IsHead {
			out = append(out, bodyGrafts(c)...)
		}
	}
	return out
}

// childDepth is the shared graft/body indent rule, applied identically by
// both the folded and unfolded loops in Rows.
//
// A session's turns are a PATH, not a hierarchy: turn 40 is not "inside"
// turn 39. Indenting per chain step made depth equal the turn number, so a
// real 111-turn session here pushed its last row 220 columns to the right
// and off the screen entirely. Every mockup in the spec was a four-turn
// illustration, which hid it completely.
//
// A branch is always indented one level under the turn it left (§5.3d),
// whether or not it is on the trunk: crossing into a different session is
// what makes something a branch, full stop, and the bar (not depth) is what
// still marks your own path through it. A child in the SAME session renders
// at its parent's depth, unless it is the body of a section (n is a head, c
// is not).
func childDepth(n, c *tree.Node, depth int) int {
	switch {
	case c.SessionID != n.SessionID:
		return depth + 1 // a branch, wherever it sits relative to the trunk
	case n.IsHead && !c.IsHead && !n.Superseded:
		// This section's body — but only when n itself gets a row to indent
		// under. n.Superseded means n is a branch's copy of a turn its
		// parent already shows (§5.3b): it renders nothing, so its body (the
		// branch's own first turns) must not be indented a second time
		// under a row that was never drawn.
		return depth + 1
	}
	return depth
}

// Rows flattens the visible forest depth-first.
//
// The visited guard is not theatre: graft edges live in a plain JSON file the
// user can hand-edit, and a cyclic pair of edges would make this walk run
// forever — a frozen overlay with no error. Build itself cannot hang (it is a
// flat pass), so this is the only place the guard is needed. Truncating a
// corrupt tree beats hanging on one.
func (m *Model) Rows() []Row {
	var out []Row
	visited := map[*tree.Node]bool{}
	var walk func(n *tree.Node, depth int, nOnTrunk bool)
	walk = func(n *tree.Node, depth int, nOnTrunk bool) {
		if visited[n] {
			return
		}
		visited[n] = true
		// A Superseded node never has a row, so it can never be reached to
		// unfold, and folding it would bury its own body permanently. Belt
		// and braces alongside New() never setting m.Folded for one.
		folded := m.Folded[n] && !n.Superseded
		shown := m.shows(n)
		if shown {
			out = append(out, Row{
				Node: n, Depth: depth,
				HasChildren: len(n.Children) > 0,
				Folded:      folded,
				BodyCount:   bodyCount(n),
				OnTrunk:     nOnTrunk,
			})
		}
		order, onTrunkOf := m.orderedChildren(n, nOnTrunk)
		if folded {
			// Folding a section hides its body, but the chain of section
			// heads must keep going — otherwise a folded prompt would take
			// every later prompt in the session down with it. A grafted child
			// is a different branch entirely, not part of this node's body, so
			// a fold never hides it either; it gets the same graft-indent
			// treatment as the unfolded case below.
			for _, c := range order {
				if c.SessionID == n.SessionID && !c.IsHead {
					continue // folding hides this node's own body
				}
				walk(c, childDepth(n, c, depth), onTrunkOf[c])
			}
			return
		}
		// A hidden (filtered) node still does not hide its children.
		for _, c := range order {
			walk(c, childDepth(n, c, depth), onTrunkOf[c])
		}
	}
	for _, r := range m.Roots {
		walk(r, 0, m.onTrunk(r))
	}
	if from, to, ok := m.rangeIndices(out); ok {
		for i := from; i <= to; i++ {
			out[i].InRange = true
		}
	}
	return out
}

func (m *Model) clamp() {
	n := len(m.Rows())
	if n == 0 {
		m.Cursor = 0
		return
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor > n-1 {
		m.Cursor = n - 1
	}
}

func (m *Model) Down() { m.Cursor++; m.clamp() }
func (m *Model) Up()   { m.Cursor--; m.clamp() }

// Selected is the node under the cursor, or nil when the forest is empty.
func (m *Model) Selected() *tree.Node {
	rows := m.Rows()
	if len(rows) == 0 {
		return nil
	}
	m.clamp()
	return rows[m.Cursor].Node
}

// Fold collapses the selected node, or jumps to its parent when it is a leaf.
func (m *Model) Fold() {
	n := m.Selected()
	if n == nil {
		return
	}
	if len(n.Children) > 0 && !m.Folded[n] {
		m.Folded[n] = true
		return
	}
	// Jump to the row drawn one level out: the nearest row above with a
	// smaller Depth. That is what the screen shows as the parent, whatever
	// the graph says (a lifted graft, a Superseded or folded-away ancestor).
	rows := m.Rows()
	for i := m.Cursor - 1; i >= 0; i-- {
		if rows[i].Depth < rows[m.Cursor].Depth {
			m.Cursor = i
			return
		}
	}
}

// RevealTip puts the cursor on session sid's last turn, first unfolding the
// section that hides it (a line's tip is usually a reply, in a folded body).
// Every other section keeps its fold.
func (m *Model) RevealTip(sid string) {
	var tip *tree.Node
	for n := range m.parent {
		if n.SessionID == sid && n.IsSessionLeaf {
			tip = n
		}
	}
	for _, r := range m.Roots {
		if r.SessionID == sid && r.IsSessionLeaf {
			tip = r
		}
	}
	if tip == nil {
		return
	}
	// Rows() lets a folded node hide only its own session's body entries.
	for c, p := tip, m.parent[tip]; p != nil; c, p = p, m.parent[p] {
		if c.SessionID == p.SessionID && !c.IsHead {
			delete(m.Folded, p)
		}
	}
	for i, r := range m.Rows() {
		if r.Node == tip {
			m.Cursor = i
			return
		}
	}
}

// Unfold expands the selected node.
func (m *Model) Unfold() {
	if n := m.Selected(); n != nil {
		delete(m.Folded, n)
	}
}
