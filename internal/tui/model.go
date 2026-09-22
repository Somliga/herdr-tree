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

	parent map[*tree.Node]*tree.Node
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
// ScopeTo narrows the forest to one session's own tree: its turns, plus any
// session grafted from it, nested.
//
// This is the default because it is the actual workflow — go back to an
// earlier point in THIS conversation and branch from it. A repo-wide forest
// is the rarer case (looking back at an old branch), and on a real repo it is
// dozens of sessions of somebody else's turns between you and the one you
// wanted. Pi's tree is one session for the same reason.
//
// Returns nil when the session is not in the forest, so the caller can fall
// back to showing everything rather than showing nothing.
func ScopeTo(roots []*tree.Node, sessionID string) []*tree.Node {
	if sessionID == "" {
		return nil
	}
	var found *tree.Node
	var walk func(n *tree.Node)
	seen := map[*tree.Node]bool{}
	walk = func(n *tree.Node) {
		if found != nil || seen[n] {
			return
		}
		seen[n] = true
		if n.IsSessionRoot && n.SessionID == sessionID {
			found = n
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	if found == nil {
		return nil
	}
	return []*tree.Node{found}
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
	for n := range m.parent {
		if n.IsHead && len(n.Children) > 0 {
			m.Folded[n] = true
		}
	}
	for _, r := range roots {
		if r.IsHead && len(r.Children) > 0 {
			m.Folded[r] = true
		}
	}
	return m
}

// orderedChildren returns n's children in render order — this turn's own
// body first, then anything that diverges here, then whatever continues the
// line last — plus, for each child, whether it continues the trunk (a graft
// whose session is in m.OnTrunk when n itself is on the trunk, otherwise the
// same-session chain child; neither exists as "the trunk" when n is off it,
// but the same child is still picked out as the line's continuation for
// ordering purposes).
//
// Putting the divergence before the continuation is what makes a branch
// render immediately under the turn it left rather than after the whole
// remaining trunk — Build appends a graft as the LAST child of the node it
// left, so walking children in Build's own order drags the rest of the trunk
// between a divergence and its branch. This is the one place that ordering
// is decided, shared by both the folded and unfolded loops in Rows, so the
// rule is never duplicated.
func (m *Model) orderedChildren(n *tree.Node, nOnTrunk bool) (order []*tree.Node, onTrunkOf map[*tree.Node]bool) {
	var cont *tree.Node
	if nOnTrunk {
		for _, c := range n.Children {
			if c.SessionID != n.SessionID && m.OnTrunk[c.SessionID] {
				cont = c
				break
			}
		}
	}
	if cont == nil {
		for _, c := range n.Children {
			if c.IsHead && c.SessionID == n.SessionID {
				cont = c
				break
			}
		}
	}

	var body, diverge []*tree.Node
	onTrunkOf = map[*tree.Node]bool{}
	for _, c := range n.Children {
		switch {
		case c.SessionID == n.SessionID && !c.IsHead:
			// This turn's own body: stays on-trunk exactly when n does,
			// regardless of which child (if any) continues the line.
			body = append(body, c)
			onTrunkOf[c] = nOnTrunk
		case c == cont:
			onTrunkOf[c] = nOnTrunk
		default:
			diverge = append(diverge, c)
			onTrunkOf[c] = false
		}
	}
	order = append(order, body...)
	order = append(order, diverge...)
	if cont != nil {
		order = append(order, cont)
	}
	return order, onTrunkOf
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
// A child in the SAME session renders at its parent's depth, unless it is
// the body of a section (n is a head, c is not). A child starting a
// DIFFERENT session — which only happens via a graft edge — earns an indent
// too, UNLESS it continues the trunk: crossing into the session you are
// actually on is not a branch, it IS the main line, and whatever the parent
// leaves behind — the abandoned tail after a rewind, say — is the thing
// that reads as the branch instead.
func childDepth(n, c *tree.Node, depth int, nOnTrunk, cOnTrunk bool) int {
	switch {
	case nOnTrunk && !cOnTrunk:
		return depth + 1 // the divergence: a branch, or the abandoned tail
	case !nOnTrunk && c.SessionID != n.SessionID:
		return depth + 1 // v1 graft indent, off-trunk throughout
	case n.IsHead && !c.IsHead:
		return depth + 1 // this section's body
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
		folded := m.Folded[n]
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
				walk(c, childDepth(n, c, depth, nOnTrunk, onTrunkOf[c]), onTrunkOf[c])
			}
			return
		}
		// A hidden (filtered) node still does not hide its children.
		for _, c := range order {
			walk(c, childDepth(n, c, depth, nOnTrunk, onTrunkOf[c]), onTrunkOf[c])
		}
	}
	for _, r := range m.Roots {
		walk(r, 0, m.onTrunk(r))
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
	p := m.parent[n]
	if p == nil {
		return
	}
	for i, r := range m.Rows() {
		if r.Node == p {
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
