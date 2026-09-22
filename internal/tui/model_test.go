package tui

import (
	"fmt"
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
	// A session's turns are a path, not a hierarchy: turn 3 is not "inside"
	// turn 2, so same-session steps do not indent.
	if m.Rows()[2].Depth != 0 {
		t.Fatalf("depth %d want 0 (same session, no graft)", m.Rows()[2].Depth)
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

// graftChain builds a chain like chain() but under its own session id, and
// attaches it as a child of parent — the one thing that is supposed to earn
// an indent.
func graftChain(sessionID string, parent *tree.Node, ids ...string) *tree.Node {
	var head, prev *tree.Node
	for i, id := range ids {
		n := &tree.Node{
			Node:          adapter.Node{ID: id, Title: "turn " + id},
			SessionID:     sessionID,
			IsSessionRoot: i == 0,
			Grafted:       i == 0,
		}
		if prev == nil {
			head = n
		} else {
			prev.Children = append(prev.Children, n)
		}
		prev = n
	}
	parent.Children = append(parent.Children, head)
	return head
}

func TestALongSingleSessionRendersEveryRowAtDepthZero(t *testing.T) {
	// A realistic count, not 3: the per-turn-indent bug only becomes visible
	// at scale. A real session in this repo has 111 turns; its last row
	// indented 220 columns off screen before this fix.
	ids := make([]string, 111)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i+1)
	}
	m := New([]*tree.Node{chain(ids...)})
	rows := m.Rows()
	if len(rows) != 111 {
		t.Fatalf("got %d rows want 111", len(rows))
	}
	maxDepth := 0
	for _, r := range rows {
		if r.Depth > maxDepth {
			maxDepth = r.Depth
		}
	}
	if maxDepth != 0 {
		t.Fatalf("max depth %d want 0: a session's turns are a path, not a hierarchy (last row's depth was %d)", maxDepth, rows[len(rows)-1].Depth)
	}
}

func TestGraftedSessionIndentsOneLevelAndStaysThere(t *testing.T) {
	root := chain("n1", "n2", "n3")
	child := graftChain("s2", root.Children[0].Children[0], "m1", "m2", "m3") // grafted from n3

	m := New([]*tree.Node{root})
	byID := map[string]Row{}
	for _, r := range m.Rows() {
		byID[r.Node.Node.ID] = r
	}
	for _, id := range []string{"n1", "n2", "n3"} {
		if byID[id].Depth != 0 {
			t.Fatalf("%s at depth %d want 0", id, byID[id].Depth)
		}
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if byID[id].Depth != 1 {
			t.Fatalf("%s at depth %d want 1: grafted session should sit one level deeper, and stay there for every turn of it", id, byID[id].Depth)
		}
	}
	_ = child
}

func TestTwoGraftsFromTheSameTurnShareADepth(t *testing.T) {
	root := chain("n1")
	graftChain("first", root, "a1")
	graftChain("second", root, "b1")

	m := New([]*tree.Node{root})
	byID := map[string]Row{}
	for _, r := range m.Rows() {
		byID[r.Node.Node.ID] = r
	}
	if byID["a1"].Depth != byID["b1"].Depth {
		t.Fatalf("two branches taken from the same turn should sit at the same depth: a1=%d b1=%d", byID["a1"].Depth, byID["b1"].Depth)
	}
	if byID["a1"].Depth != 1 {
		t.Fatalf("depth %d want 1", byID["a1"].Depth)
	}
}

func TestMaxDepthTracksGraftNestingNotTurnCount(t *testing.T) {
	// This is the property that actually broke: depth used to equal the turn
	// number, so a long session alone produced a deep tree. It must instead
	// track how many graft edges are nested, which here is 2 regardless of
	// how many turns each session has.
	root := chain("n1", "n2", "n3", "n4", "n5") // 5 turns, no grafts: depth must stay 0
	mid := graftChain("s2", root.Children[0].Children[0].Children[0].Children[0], "m1", "m2", "m3", "m4", "m5", "m6") // grafted, +6 turns
	graftChain("s3", mid.Children[0].Children[0], "o1", "o2") // grafted again, one level deeper

	m := New([]*tree.Node{root})
	max := 0
	for _, r := range m.Rows() {
		if r.Depth > max {
			max = r.Depth
		}
	}
	if max != 2 {
		t.Fatalf("max depth %d want 2 — two nested graft edges, not the 13-turn total", max)
	}
}

func TestScopeToReturnsOnlyTheNamedSessionPlusItsGraftedChildren(t *testing.T) {
	s1 := chain("n1", "n2")
	graftChain("s2", s1.Children[0], "m1", "m2") // grafted from n2, session s2
	s3 := &tree.Node{                            // unrelated third session
		Node: adapter.Node{ID: "o1", Title: "turn o1"}, SessionID: "s3", IsSessionRoot: true,
	}

	scoped := ScopeTo([]*tree.Node{s1, s3}, "s1")
	if len(scoped) != 1 || scoped[0] != s1 {
		t.Fatalf("ScopeTo should return exactly s1's root, got %+v", scoped)
	}
	m := New(scoped)
	got := ids(m.Rows())
	want := map[string]bool{"n1": true, "n2": true, "m1": true, "m2": true}
	if len(got) != len(want) {
		t.Fatalf("got %v want the s1 chain plus the session grafted from it", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected node %q in scoped view (s3 must not leak in): %v", id, got)
		}
	}
}

func TestScopeToUnknownSessionReturnsNil(t *testing.T) {
	s1 := chain("n1", "n2")
	if got := ScopeTo([]*tree.Node{s1}, "does-not-exist"); got != nil {
		t.Fatalf("want nil for an unknown session so the caller can fall back to the full forest, got %+v", got)
	}
}

func TestScopeToEmptySessionIDReturnsNil(t *testing.T) {
	s1 := chain("n1")
	if got := ScopeTo([]*tree.Node{s1}, ""); got != nil {
		t.Fatalf("want nil for an empty session id, got %+v", got)
	}
}

func TestGraftedChildIndentsOneLevelInAScopedView(t *testing.T) {
	s1 := chain("n1", "n2")
	graftChain("s2", s1.Children[0], "m1")

	scoped := ScopeTo([]*tree.Node{s1}, "s1")
	m := New(scoped)
	byID := map[string]Row{}
	for _, r := range m.Rows() {
		byID[r.Node.Node.ID] = r
	}
	if byID["n1"].Depth != 0 || byID["n2"].Depth != 0 {
		t.Fatalf("s1's own turns should stay at depth 0 in the scoped view: n1=%d n2=%d", byID["n1"].Depth, byID["n2"].Depth)
	}
	if byID["m1"].Depth != 1 {
		t.Fatalf("grafted child depth %d want 1", byID["m1"].Depth)
	}
}

