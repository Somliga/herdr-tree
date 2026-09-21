// Package tui renders the conversation forest. The navigation model is kept
// separate from rendering so it can be tested as plain functions.
package tui

import "herdr-tree/internal/tree"

type Row struct {
	Node        *tree.Node
	Depth       int
	HasChildren bool
	Folded      bool
}

// Density is how much of the tree is shown. Cycling it is the answer to a
// repo with hundreds of sessions: folding hides a subtree you chose, density
// hides a KIND of row everywhere at once. Borrowed from Pi, whose tree view
// cycles the same way and which is where this plugin's model came from.
type Density int

const (
	DensityAll      Density = iota // every turn
	DensityLabelled                // only labelled turns, plus session roots
	DensityRoots                   // one row per session
)

func (d Density) String() string {
	switch d {
	case DensityLabelled:
		return "labelled"
	case DensityRoots:
		return "sessions"
	default:
		return "all"
	}
}

type Model struct {
	Roots   []*tree.Node
	Cursor  int
	Folded  map[*tree.Node]bool
	Density Density

	parent map[*tree.Node]*tree.Node
}

// CycleDensity advances to the next density, keeping the selected node visible
// where it still can be. A view control that loses your place is worse than no
// view control.
func (m *Model) CycleDensity() {
	was := m.Selected()
	m.Density = (m.Density + 1) % 3
	if was == nil {
		return
	}
	for i, r := range m.Rows() {
		if r.Node == was {
			m.Cursor = i
			return
		}
	}
	// the selected row is hidden at this density: fall back to its nearest
	// visible ancestor rather than jumping to an unrelated row.
	for n := m.parent[was]; n != nil; n = m.parent[n] {
		for i, r := range m.Rows() {
			if r.Node == n {
				m.Cursor = i
				return
			}
		}
	}
	m.Cursor = 0
	m.clamp()
}

// visible reports whether a node is shown at the current density. A session
// root is always shown — hiding one would lose the session entirely, which is
// the failure this whole design exists to avoid.
func (m *Model) visible(n *tree.Node) bool {
	if n.IsSessionRoot {
		return true
	}
	switch m.Density {
	case DensityRoots:
		return false
	case DensityLabelled:
		return n.Label != ""
	default:
		return true
	}
}

// New indexes each node's parent so Fold can jump upward.
//
// The "already seen" check is the same cycle defence as Rows(), and it is
// needed here for a harsher reason: an unguarded recursive walk over a cyclic
// tree overflows the stack, and a Go stack overflow is a fatal error that no
// recover can catch. That kills the plugin process outright rather than
// merely freezing the view. Graft edges live in a plain JSON file that can be
// hand-edited or corrupted into a cycle, so this is reachable.
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
	return m
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
	var walk func(n *tree.Node, depth int)
	walk = func(n *tree.Node, depth int) {
		if visited[n] {
			return
		}
		visited[n] = true
		folded := m.Folded[n]
		shown := m.visible(n)
		if shown {
			out = append(out, Row{
				Node: n, Depth: depth,
				HasChildren: len(n.Children) > 0,
				Folded:      folded,
			})
			if folded {
				return
			}
		}
		// A hidden node does not hide its children: descend at the same depth
		// so a labelled turn deep in a session still appears, rather than
		// disappearing with its parents.
		next := depth
		if shown {
			next = depth + 1
		}
		for _, c := range n.Children {
			walk(c, next)
		}
	}
	for _, r := range m.Roots {
		walk(r, 0)
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
