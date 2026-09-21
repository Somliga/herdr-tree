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

type Model struct {
	Roots  []*tree.Node
	Cursor int
	Folded map[*tree.Node]bool

	parent map[*tree.Node]*tree.Node
}

func New(roots []*tree.Node) *Model {
	m := &Model{Roots: roots, Folded: map[*tree.Node]bool{}, parent: map[*tree.Node]*tree.Node{}}
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		for _, c := range n.Children {
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
		out = append(out, Row{
			Node: n, Depth: depth,
			HasChildren: len(n.Children) > 0,
			Folded:      folded,
		})
		if folded {
			return
		}
		for _, c := range n.Children {
			walk(c, depth+1)
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
