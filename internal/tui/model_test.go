package tui

import (
	"testing"

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
