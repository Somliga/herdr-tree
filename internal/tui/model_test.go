package tui

import (
	"testing"
	"time"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

func chain(ids ...string) *tree.Node {
	var head, prev *tree.Node
	for i, id := range ids {
		n := &tree.Node{
			Node:          adapter.Node{ID: id, Title: "turn " + id},
			SessionID:     "s1",
			IsSessionRoot: i == 0,
		}
		if prev == nil {
			head = n
		} else {
			prev.Children = append(prev.Children, n)
		}
		prev = n
	}
	return head
}

func ids(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Node.Node.ID)
	}
	return out
}

func TestRowsFlattensDepthFirst(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2", "n3")})
	got := ids(m.Rows())
	want := []string{"n1", "n2", "n3"}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
	if m.Rows()[2].Depth != 2 {
		t.Fatalf("depth %d want 2", m.Rows()[2].Depth)
	}
}

func TestFoldHidesDescendants(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2", "n3")})
	m.Fold() // cursor on n1
	got := ids(m.Rows())
	if len(got) != 1 || got[0] != "n1" {
		t.Fatalf("got %v want [n1]", got)
	}
	if !m.Rows()[0].Folded {
		t.Fatal("row should report folded")
	}
	m.Unfold()
	if len(m.Rows()) != 3 {
		t.Fatalf("unfold failed: %v", ids(m.Rows()))
	}
}

func TestCursorClampsAtEnds(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	m.Up()
	if m.Cursor != 0 {
		t.Fatalf("cursor %d want 0", m.Cursor)
	}
	m.Down()
	m.Down()
	m.Down()
	if m.Cursor != 1 {
		t.Fatalf("cursor %d want 1", m.Cursor)
	}
}

func TestCyclicTreeDoesNotCrashOrHang(t *testing.T) {
	// A hand-edited or corrupted tree.json can express mutually-nesting graft
	// edges. New()'s recursive walk would overflow the stack — a fatal error
	// that kills the process — and Rows() would loop forever.
	a := &tree.Node{Node: adapter.Node{ID: "a", Title: "A"}, SessionID: "s1"}
	b := &tree.Node{Node: adapter.Node{ID: "b", Title: "B"}, SessionID: "s2"}
	a.Children = append(a.Children, b)
	b.Children = append(b.Children, a)

	done := make(chan int, 1)
	go func() { done <- len(New([]*tree.Node{a}).Rows()) }()
	select {
	case n := <-done:
		if n == 0 {
			t.Fatal("want at least the reachable nodes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cyclic tree hangs: the overlay would freeze with no error")
	}
}

func TestFoldOnLeafMovesToParent(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	m.Down() // on n2, a leaf
	m.Fold()
	if m.Cursor != 0 {
		t.Fatalf("folding a leaf should move to its parent; cursor %d", m.Cursor)
	}
}

func TestSelectedTracksCursor(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	m.Down()
	if m.Selected().Node.ID != "n2" {
		t.Fatalf("got %q", m.Selected().Node.ID)
	}
}

func TestEmptyForestHasNoSelection(t *testing.T) {
	m := New(nil)
	if len(m.Rows()) != 0 {
		t.Fatal("want no rows")
	}
	if m.Selected() != nil {
		t.Fatal("want nil selection")
	}
}

func TestCycleDensityReturnsToStartAndKeepsRootVisible(t *testing.T) {
	root := chain("n1", "n2", "n3")
	m := New([]*tree.Node{root})
	if m.Density != DensityAll {
		t.Fatalf("Density = %v want DensityAll", m.Density)
	}
	for i := 0; i < 3; i++ {
		m.CycleDensity()
		var sawRoot bool
		for _, r := range m.Rows() {
			if r.Node == root {
				sawRoot = true
			}
		}
		if !sawRoot {
			t.Fatalf("session root hidden at density %v", m.Density)
		}
	}
	if m.Density != DensityAll {
		t.Fatalf("after three cycles Density = %v want DensityAll", m.Density)
	}
}

func TestLabelledDensityShowsABuriedLabelledTurn(t *testing.T) {
	root := chain("n1", "n2", "n3")
	buried := root.Children[0].Children[0] // n3, under unlabelled n2
	buried.Label = "landmark"

	m := New([]*tree.Node{root})
	m.Density = DensityLabelled
	got := ids(m.Rows())
	var sawBuried bool
	for _, id := range got {
		if id == "n3" {
			sawBuried = true
		}
	}
	if !sawBuried {
		t.Fatalf("labelled turn buried under unlabelled parents did not render: %v", got)
	}
	for _, id := range got {
		if id == "n2" {
			t.Fatalf("unlabelled, non-root turn should not render at DensityLabelled: %v", got)
		}
	}
}

func TestCycleDensityKeepsCursorOnAStillVisibleNode(t *testing.T) {
	root := chain("n1", "n2", "n3") // root is always visible at every density
	m := New([]*tree.Node{root})
	if m.Cursor != 0 || m.Selected() != root {
		t.Fatalf("setup: cursor should start on the root")
	}
	m.CycleDensity()
	if m.Selected() != root {
		t.Fatalf("cursor moved off a node that remained visible: now on %+v", m.Selected())
	}
}
