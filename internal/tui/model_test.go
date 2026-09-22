package tui

import (
	"fmt"
	"testing"
	"time"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
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

func TestWindowKeepsSelectionVisibleAtTopMiddleAndBottom(t *testing.T) {
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i+1)
	}
	m := New([]*tree.Node{chain(ids...)})

	m.Cursor = 0
	rows, start, total := m.Window(10)
	if total != 50 {
		t.Fatalf("total %d want 50", total)
	}
	if m.Cursor < start || m.Cursor >= start+len(rows) {
		t.Fatalf("cursor %d not within window [%d,%d)", m.Cursor, start, start+len(rows))
	}

	m.Cursor = 25
	rows, start, _ = m.Window(10)
	if m.Cursor < start || m.Cursor >= start+len(rows) {
		t.Fatalf("cursor %d not within window [%d,%d)", m.Cursor, start, start+len(rows))
	}

	m.Cursor = 49
	rows, start, _ = m.Window(10)
	if m.Cursor < start || m.Cursor >= start+len(rows) {
		t.Fatalf("cursor %d not within window [%d,%d)", m.Cursor, start, start+len(rows))
	}
	if start+len(rows) != 50 {
		t.Fatalf("window does not reach the end: start=%d len=%d", start, len(rows))
	}
}

func TestWindowReturnsEverythingWhenItFits(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	rows, start, total := m.Window(10)
	if start != 0 || total != 2 || len(rows) != 2 {
		t.Fatalf("got rows=%d start=%d total=%d want 2,0,2", len(rows), start, total)
	}
}

func TestFilterHumanHidesNonHumanRowsButKeepsForkStructure(t *testing.T) {
	root := chain("n1")
	root.Children = append(root.Children, &tree.Node{
		Node:      adapter.Node{ID: "n2", Title: "tool call", Kind: adapter.KindToolCall},
		SessionID: "s1",
	})
	leaf := &tree.Node{Node: adapter.Node{ID: "n3", Title: "human again", Kind: adapter.KindHuman}, SessionID: "s1"}
	root.Children[0].Children = append(root.Children[0].Children, leaf)

	m := New([]*tree.Node{root})
	m.Filter = FilterHuman
	got := ids(m.Rows())
	want := []string{"n1", "n3"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v: hidden node must still be descended into so its child is reachable", got, want)
	}
}

func TestCycleFilterTogglesAndBack(t *testing.T) {
	m := New([]*tree.Node{chain("n1")})
	if m.Filter != FilterDefault {
		t.Fatalf("default filter %v want FilterDefault", m.Filter)
	}
	m.CycleFilter()
	if m.Filter != FilterHuman {
		t.Fatalf("filter %v want FilterHuman", m.Filter)
	}
	m.CycleFilter()
	if m.Filter != FilterDefault {
		t.Fatalf("filter %v want FilterDefault", m.Filter)
	}
}

// realisticSession builds a session with `heads` human prompts whose bodies
// (assistant replies and tool calls) sum to `bodyTotal`, distributed as
// evenly as the division allows — the scale at which the depth-equals-turn
// and flat-wall-of-rows bugs actually show up, not a 3-turn mockup.
func realisticSession(id string, heads, bodyTotal int) adapter.Session {
	s := adapter.Session{ID: id, Title: "t-" + id}
	remaining := bodyTotal
	for h := 0; h < heads; h++ {
		s.Nodes = append(s.Nodes, adapter.Node{ID: fmt.Sprintf("h%d", h), Title: "prompt", Kind: adapter.KindHuman})
		left := heads - h
		n := remaining / left
		for b := 0; b < n; b++ {
			kind := adapter.KindAssistant
			if b%2 == 1 {
				kind = adapter.KindToolCall
			}
			s.Nodes = append(s.Nodes, adapter.Node{ID: fmt.Sprintf("h%d-b%d", h, b), Title: "reply", Kind: kind})
		}
		remaining -= n
	}
	return s
}

func TestRealisticSessionFoldsToJustItsPromptsAndUnfoldsToEverything(t *testing.T) {
	sess := realisticSession("s1", 19, 660)
	roots := tree.Build([]adapter.Session{sess}, &store.Store{Branches: map[string]store.Branch{}})
	m := New(roots)

	folded := m.Rows()
	if len(folded) != 19 {
		t.Fatalf("folded rows = %d want 19", len(folded))
	}
	for _, r := range folded {
		if r.Depth != 0 {
			t.Fatalf("folded row %q at depth %d want 0: every head must sit level regardless of session length", r.Node.Node.ID, r.Depth)
		}
		if !r.Node.IsHead {
			t.Fatalf("row %q visible while folded but is not a head", r.Node.Node.ID)
		}
	}

	for n := range m.Folded {
		delete(m.Folded, n)
	}
	unfolded := m.Rows()
	if len(unfolded) != 679 {
		t.Fatalf("unfolded rows = %d want 679 (19 heads + 660 body)", len(unfolded))
	}
	max := 0
	for _, r := range unfolded {
		if r.Depth > max {
			max = r.Depth
		}
		if !r.Node.IsHead && r.Depth != 1 {
			t.Fatalf("body row %q at depth %d want 1", r.Node.Node.ID, r.Depth)
		}
		if r.Node.IsHead && r.Depth != 0 {
			t.Fatalf("head row %q at depth %d want 0", r.Node.Node.ID, r.Depth)
		}
	}
	if max != 1 {
		t.Fatalf("max depth unfolded = %d want 1", max)
	}
}

func TestFoldedHeadReportsItsBodySize(t *testing.T) {
	sess := realisticSession("s1", 19, 660)
	roots := tree.Build([]adapter.Session{sess}, &store.Store{Branches: map[string]store.Branch{}})
	m := New(roots)
	rows := m.Rows()
	total := 0
	for _, r := range rows {
		if !r.Folded || !r.HasChildren {
			t.Fatalf("row %q should be a folded head with children", r.Node.Node.ID)
		}
		total += r.BodyCount
	}
	if total != 660 {
		t.Fatalf("sum of BodyCount across all folded heads = %d want 660", total)
	}
}

func TestGraftFromABodyTurnIndentsOneLevelFurtherThanTheBody(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "h0-b0"}})
	sess1 := realisticSession("s1", 2, 4) // small: h0,h0-b0,h0-b1,h1,h1-b0
	sess2 := adapter.Session{ID: "s2", Title: "t-s2", Nodes: []adapter.Node{{ID: "m1", Title: "branch", Kind: adapter.KindHuman}}}

	roots := tree.Build([]adapter.Session{sess1, sess2}, st)
	m := New(roots)
	for n := range m.Folded {
		delete(m.Folded, n)
	}
	byID := map[string]Row{}
	for _, r := range m.Rows() {
		byID[r.Node.Node.ID] = r
	}
	if byID["h0-b0"].Depth != 1 {
		t.Fatalf("h0-b0 (body) depth %d want 1", byID["h0-b0"].Depth)
	}
	if byID["m1"].Depth != 2 {
		t.Fatalf("grafted session should sit one level deeper than the body turn it branched from: got %d want 2", byID["m1"].Depth)
	}
}

func TestSelectStillCarriesEveryEntryDespiteFolding(t *testing.T) {
	// Select (internal/claude/graft.go) is untouched by this change: the
	// asymmetry between what the tree hides and what a graft carries is
	// load-bearing. This just confirms the tree's own node count into a
	// graft point is unaffected by section folding — Select walks
	// sess.Nodes directly, never the *tree.Node forest, so folding cannot
	// reach it.
	sess := realisticSession("s1", 19, 660)
	if len(sess.Nodes) != 679 {
		t.Fatalf("session has %d raw nodes want 679: folding must never touch the underlying transcript", len(sess.Nodes))
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


func TestTrunkRendersAtDepthZeroEvenWhenGrafted(t *testing.T) {
	// root ── (branch point) ── grafted, and the user is in the grafted one
	root := &tree.Node{Node: adapter.Node{ID: "r1"}, SessionID: "root", IsSessionRoot: true, IsHead: true}
	r2 := &tree.Node{Node: adapter.Node{ID: "r2"}, SessionID: "root", IsHead: true}
	g1 := &tree.Node{Node: adapter.Node{ID: "g1"}, SessionID: "graft", IsSessionRoot: true, IsHead: true, Grafted: true}
	g2 := &tree.Node{Node: adapter.Node{ID: "g2"}, SessionID: "graft", IsHead: true}
	root.Children = append(root.Children, r2)
	r2.Children = append(r2.Children, g1)
	g1.Children = append(g1.Children, g2)

	m := New([]*tree.Node{root})
	m.SetTrunk(map[string]bool{"graft": true, "root": true})
	for _, r := range m.Rows() {
		if r.Node.SessionID == "graft" && r.Depth != 0 {
			t.Fatalf("the trunk must render at depth 0; %s is at %d", r.Node.Node.ID, r.Depth)
		}
	}
}

func TestOffTrunkBranchIsIndentedAtItsDivergence(t *testing.T) {
	root := &tree.Node{Node: adapter.Node{ID: "r1"}, SessionID: "root", IsSessionRoot: true, IsHead: true}
	side := &tree.Node{Node: adapter.Node{ID: "s1"}, SessionID: "side", IsSessionRoot: true, IsHead: true, Grafted: true}
	root.Children = append(root.Children, side)

	m := New([]*tree.Node{root})
	m.SetTrunk(map[string]bool{"root": true})
	var sideDepth = -1
	for _, r := range m.Rows() {
		if r.Node.SessionID == "side" {
			sideDepth = r.Depth
		}
		if r.Node.SessionID == "root" && !r.OnTrunk {
			t.Fatal("root is on the trunk and the row should say so")
		}
	}
	if sideDepth != 1 {
		t.Fatalf("an off-trunk branch indents once; got %d", sideDepth)
	}
}

func TestNoTrunkFallsBackToV1(t *testing.T) {
	root := &tree.Node{Node: adapter.Node{ID: "r1"}, SessionID: "root", IsSessionRoot: true, IsHead: true}
	g := &tree.Node{Node: adapter.Node{ID: "g1"}, SessionID: "graft", IsSessionRoot: true, IsHead: true, Grafted: true}
	root.Children = append(root.Children, g)
	m := New([]*tree.Node{root})
	m.SetTrunk(nil) // no live session
	depths := map[string]int{}
	for _, r := range m.Rows() {
		depths[r.Node.SessionID] = r.Depth
	}
	if depths["graft"] != 1 {
		t.Fatalf("with no trunk the v1 shape stands; graft at %d want 1", depths["graft"])
	}
}
