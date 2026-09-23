package tree

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

func sess(id string, nodes ...string) adapter.Session {
	s := adapter.Session{ID: id, Title: "t-" + id, Updated: time.Now()}
	for _, n := range nodes {
		s.Nodes = append(s.Nodes, adapter.Node{ID: n, Title: "turn " + n})
	}
	return s
}

func TestLinearSessionBecomesAChain(t *testing.T) {
	roots := Build([]adapter.Session{sess("s1", "n1", "n2", "n3")}, emptyStore())
	if len(roots) != 1 {
		t.Fatalf("roots %d", len(roots))
	}
	n := roots[0]
	if n.Node.ID != "n1" || !n.IsSessionRoot {
		t.Fatalf("root %+v", n)
	}
	if len(n.Children) != 1 || n.Children[0].Node.ID != "n2" {
		t.Fatal("n2 should hang off n1")
	}
	if len(n.Children[0].Children) != 1 || n.Children[0].Children[0].Node.ID != "n3" {
		t.Fatal("n3 should hang off n2")
	}
}

func TestGraftEdgeNestsChildSessionUnderParentTurn(t *testing.T) {
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n2"}})

	roots := Build([]adapter.Session{sess("s1", "n1", "n2", "n3"), sess("s2", "m1", "m2")}, st)
	if len(roots) != 1 {
		t.Fatalf("grafted session must not be a root: %d roots", len(roots))
	}
	n2 := roots[0].Children[0]
	if n2.Node.ID != "n2" {
		t.Fatalf("expected n2, got %s", n2.Node.ID)
	}
	var ids []string
	for _, c := range n2.Children {
		ids = append(ids, c.Node.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("n2 children %v want n3 and m1", ids)
	}
	var grafted *Node
	for _, c := range n2.Children {
		if c.Grafted {
			grafted = c
		}
	}
	if grafted == nil || grafted.Node.ID != "m1" {
		t.Fatalf("m1 should be the grafted child, got %+v", grafted)
	}
	if !grafted.IsSessionRoot || grafted.SessionID != "s2" {
		t.Fatalf("grafted child metadata wrong: %+v", grafted)
	}
}

func TestDanglingGraftParentFallsBackToRoot(t *testing.T) {
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "gone", Node: "nope"}})
	roots := Build([]adapter.Session{sess("s2", "m1")}, st)
	if len(roots) != 1 || roots[0].Node.ID != "m1" {
		t.Fatalf("a session whose parent vanished must still render: %+v", roots)
	}
}

func TestBuildCarriesTheSessionPath(t *testing.T) {
	// Without this the TUI rebuilds a Session with no Path, and Preview and
	// Branch silently fall back to reconstructing it — wrong for any session
	// that relocated into a worktree.
	s := sess("s1", "n1", "n2")
	s.Path = "/somewhere/-odd-project-dir/s1.jsonl"
	roots := Build([]adapter.Session{s}, emptyStore())
	if roots[0].SessionPath != s.Path {
		t.Fatalf("root SessionPath = %q want %q", roots[0].SessionPath, s.Path)
	}
	if roots[0].Children[0].SessionPath != s.Path {
		t.Fatalf("child SessionPath = %q want %q", roots[0].Children[0].SessionPath, s.Path)
	}
}

func TestGraftedSiblingsRenderInAStableOrder(t *testing.T) {
	// Two branches taken from the SAME turn. Map iteration order is randomised
	// per run, so without explicit ordering these two swap places between
	// launches of a tree the user navigates by position.
	sessions := []adapter.Session{
		sess("s1", "n1", "n2"),
		sess("first", "a1"),
		sess("second", "b1"),
	}
	var seen []string
	for i := 0; i < 20; i++ {
		st := emptyStore()
		st.Add("first", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})
		st.Add("second", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})

		roots := Build(sessions, st)
		var order []string
		for _, c := range roots[0].Children {
			order = append(order, c.SessionID)
		}
		got := strings.Join(order, ",")
		if i == 0 {
			seen = order
			continue
		}
		if got != strings.Join(seen, ",") {
			t.Fatalf("grafted sibling order changed between runs: %v then %v", seen, order)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("want n2 plus both grafted children under n1, got %v", seen)
	}
}

func TestGraftParentTurnGoneFallsBackToRoot(t *testing.T) {
	// The parent session is present and readable, but the specific turn the
	// branch was taken from is no longer there.
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "vanished"}})

	roots := Build([]adapter.Session{sess("s1", "n1", "n2"), sess("s2", "m1")}, st)
	if len(roots) != 2 {
		t.Fatalf("want both sessions as roots, got %d: %+v", len(roots), roots)
	}
	var sawChild bool
	for _, r := range roots {
		if r.SessionID == "s2" && r.Node.ID == "m1" {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatal("a branch whose graft turn vanished must still render as a root, not disappear")
	}
}

func TestGraftFromABrokenParentFallsBackToRoot(t *testing.T) {
	// A parent whose transcript became unreadable has zero turns, so its node
	// index is empty and the child's graft point cannot be found.
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})

	broken := adapter.Session{ID: "s1", Title: "t-s1", Broken: true}
	roots := Build([]adapter.Session{broken, sess("s2", "m1")}, st)
	if len(roots) != 2 {
		t.Fatalf("want 2 roots (the broken parent and the orphaned child), got %d: %+v", len(roots), roots)
	}
}

func TestGraftEdgeForAMissingSessionIsIgnored(t *testing.T) {
	// The store remembers a branch whose transcript the user has since deleted.
	st := emptyStore()
	st.Add("deleted-session", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})

	roots := Build([]adapter.Session{sess("s1", "n1", "n2")}, st)
	if len(roots) != 1 || roots[0].SessionID != "s1" {
		t.Fatalf("a stale edge must not invent a node: %+v", roots)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].Node.ID != "n2" {
		t.Fatalf("the surviving session must be unaffected: %+v", roots[0].Children)
	}
}

func TestEmptySessionStillRenders(t *testing.T) {
	roots := Build([]adapter.Session{sess("s1")}, emptyStore())
	if len(roots) != 1 {
		t.Fatalf("roots %d want 1", len(roots))
	}
	if !roots[0].Broken {
		t.Fatalf("a session with no turns must be marked Broken so it renders as a warning row: %+v", roots[0])
	}
	if !roots[0].IsSessionRoot || roots[0].SessionID != "s1" {
		t.Fatalf("got %+v", roots[0])
	}
}

// sessWithKinds builds a session where each node's Kind is given explicitly,
// for testing section grouping (sess() above always builds KindHuman nodes,
// which makes every turn a head and so never exercises this).
func sessWithKinds(id string, kinds ...adapter.Kind) adapter.Session {
	s := adapter.Session{ID: id, Title: "t-" + id, Updated: time.Now()}
	for i, k := range kinds {
		s.Nodes = append(s.Nodes, adapter.Node{ID: fmt.Sprintf("n%d", i+1), Title: fmt.Sprintf("turn %d", i+1), Kind: k})
	}
	return s
}

func TestPromptHeadsASectionWithRepliesAndToolCallsAsItsBody(t *testing.T) {
	// n1 human, n2 assistant, n3 tool, n4 human, n5 assistant.
	roots := Build([]adapter.Session{sessWithKinds("s1",
		adapter.KindHuman, adapter.KindAssistant, adapter.KindToolCall, adapter.KindHuman, adapter.KindAssistant,
	)}, emptyStore())
	n1 := roots[0]
	if !n1.IsHead {
		t.Fatal("a human prompt must be a section head")
	}
	if len(n1.Children) != 3 {
		t.Fatalf("n1 children %d want 3 (n2, n3, then the next head n4)", len(n1.Children))
	}
	n2, n3, n4 := n1.Children[0], n1.Children[1], n1.Children[2]
	if n2.IsHead || n3.IsHead {
		t.Fatalf("assistant/tool-call turns must not be heads: n2.IsHead=%v n3.IsHead=%v", n2.IsHead, n3.IsHead)
	}
	if !n4.IsHead {
		t.Fatal("n4 (human) must be a section head")
	}
	if len(n4.Children) != 1 || n4.Children[0].Node.ID != "n5" || n4.Children[0].IsHead {
		t.Fatalf("n5 should be n4's body, not a head: %+v", n4.Children)
	}
}

func TestFirstTurnIsAHeadEvenWhenNotHuman(t *testing.T) {
	// A session that does not start with a human turn still needs a head to
	// hang its body off.
	roots := Build([]adapter.Session{sessWithKinds("s1", adapter.KindAssistant, adapter.KindToolCall)}, emptyStore())
	if !roots[0].IsHead {
		t.Fatal("the first turn of a session must be a head even if it is not human")
	}
}

func emptyStore() *store.Store {
	return &store.Store{Version: 1, Branches: map[string]store.Branch{}}
}

func sessionsIn(roots []*Node) map[string]bool {
	out := map[string]bool{}
	var walk func(n *Node)
	walk = func(n *Node) {
		out[n.SessionID] = true
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

func TestAReplacedLineIsHiddenAndItsReplacementShown(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut})
	roots := Build([]adapter.Session{sess("new", "t1", "t4"), sess("old", "t1", "t2", "t3", "t4")}, st)
	got := sessionsIn(roots)
	if got["old"] || !got["new"] {
		t.Fatalf("sessions shown %v, want new and not old", got)
	}
}

// If the replacement's transcript is gone, hiding the old line would lose the
// conversation from view entirely.
func TestAReplacedLineShowsAgainIfItsReplacementIsGone(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut})
	roots := Build([]adapter.Session{sess("old", "t1", "t2")}, st)
	if !sessionsIn(roots)["old"] {
		t.Fatal("old line hidden though nothing replaces it on disk")
	}
}

func TestABranchOffAReplacedLineReattachesToTheSameTurn(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{
		"br": {GraftedFrom: store.From{SessionID: "old", Node: "t4"}},
	}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCompacted})
	roots := Build([]adapter.Session{
		sess("new", "t1", "seed", "t4"), sess("old", "t1", "t2", "t3", "t4"), sess("br", "b1"),
	}, st)
	var t4 *Node
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "new" && n.Node.ID == "t4" {
			t4 = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	if t4 == nil {
		t.Fatal("t4 missing from the new line")
	}
	found := false
	for _, c := range t4.Children {
		if c.SessionID == "br" {
			found = true
		}
	}
	if !found {
		t.Fatal("the branch did not re-attach to t4 of the new line")
	}
}

func TestABranchOffARemovedTurnBecomesAMarkedRoot(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{
		"br": {GraftedFrom: store.From{SessionID: "old", Node: "t2"}},
	}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut})
	roots := Build([]adapter.Session{sess("new", "t1", "t4"), sess("old", "t1", "t2", "t4"), sess("br", "b1")}, st)
	for _, r := range roots {
		if r.SessionID == "br" {
			if !r.FromRemoved {
				t.Fatal("the orphaned branch is not marked as coming from a removed stretch")
			}
			return
		}
	}
	t.Fatal("the orphaned branch is not a root")
}

// TestABranchRendersFromWhereItDiverges is the user's own tree (§5.3b): a
// branch grafted mid-line carries copies of every turn up to its graft
// point under the same ids, and those copies must not render a second time.
func TestABranchRendersFromWhereItDiverges(t *testing.T) {
	st := emptyStore()
	st.Add("branch", store.Branch{GraftedFrom: store.From{SessionID: "trunk", Node: "BITTEREND"}})
	roots := Build([]adapter.Session{
		sess("trunk", "hello", "BING", "BITTEREND", "FAN", "BULLDOG"),
		sess("branch", "hello", "BING", "BITTEREND", "TRIPPLEDIP", "HORSE"),
	}, st)

	if len(roots) != 1 {
		t.Fatalf("the branch must not be a root: %d roots", len(roots))
	}

	byID := map[string]*Node{}
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "branch" {
			byID[n.Node.ID] = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}

	for _, id := range []string{"hello", "BING", "BITTEREND"} {
		n, ok := byID[id]
		if !ok {
			t.Fatalf("the branch's copy of %s must still be reachable (for further grafts), got none", id)
		}
		if !n.Superseded {
			t.Fatalf("the branch's copy of %s must be marked Superseded, got %+v", id, n)
		}
		if n.IsSessionRoot {
			t.Fatalf("the branch's copy of %s must not carry the session-root marker", id)
		}
	}

	start, ok := byID["TRIPPLEDIP"]
	if !ok {
		t.Fatal("TRIPPLEDIP missing from the branch")
	}
	if !start.Grafted || !start.IsSessionRoot {
		t.Fatalf("TRIPPLEDIP must be the branch's rendered start: %+v", start)
	}
	if start.Superseded {
		t.Fatal("TRIPPLEDIP is new; it must not be Superseded")
	}

	horse, ok := byID["HORSE"]
	if !ok {
		t.Fatal("HORSE missing from the branch")
	}
	if horse.Superseded || horse.IsSessionRoot || horse.Grafted {
		t.Fatalf("HORSE is an ordinary later turn: %+v", horse)
	}
	if len(start.Children) != 1 || start.Children[0] != horse {
		t.Fatalf("HORSE must hang off TRIPPLEDIP: %+v", start.Children)
	}

	// TRIPPLEDIP must actually be attached under the trunk's BITTEREND, not
	// under the branch's own (superseded) copy of it.
	var trunkBittEREnd *Node
	walk = func(n *Node) {
		if n.SessionID == "trunk" && n.Node.ID == "BITTEREND" {
			trunkBittEREnd = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	found := false
	var descends func(n *Node) bool
	descends = func(n *Node) bool {
		if n == start {
			return true
		}
		for _, c := range n.Children {
			if descends(c) {
				return true
			}
		}
		return false
	}
	for _, c := range trunkBittEREnd.Children {
		if descends(c) {
			found = true
		}
	}
	if !found {
		t.Fatalf("TRIPPLEDIP must hang (directly or via superseded copies) off the trunk's BITTEREND, children: %+v", trunkBittEREnd.Children)
	}
}

// TestABranchWithNothingOfItsOwnKeepsOneRow covers §5.3b's other case: a
// branch that is only the copied prefix (opened, nothing typed yet) must
// still show one row, so it stays reachable and enter-able.
func TestABranchWithNothingOfItsOwnKeepsOneRow(t *testing.T) {
	st := emptyStore()
	st.Add("branch", store.Branch{GraftedFrom: store.From{SessionID: "trunk", Node: "BITTEREND"}})
	roots := Build([]adapter.Session{
		sess("trunk", "hello", "BING", "BITTEREND"),
		sess("branch", "hello", "BING", "BITTEREND"),
	}, st)

	var visible []*Node
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "branch" && !n.Superseded {
			visible = append(visible, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	if len(visible) != 1 {
		t.Fatalf("want exactly one visible row for the branch, got %d: %+v", len(visible), visible)
	}
	n := visible[0]
	if n.Node.ID != "BITTEREND" || !n.Grafted || !n.IsSessionRoot {
		t.Fatalf("the one row must be the copy of the graft point, marked Grafted+IsSessionRoot: %+v", n)
	}
}

// TestABranchDedupesAgainstAReplacementLine covers §5.3b combined with §5.3:
// a branch grafted off a line that was since spliced re-attaches to the
// replacement (via Resolve) and must still dedupe against ITS ids.
func TestABranchDedupesAgainstAReplacementLine(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{
		"branch": {GraftedFrom: store.From{SessionID: "old", Node: "t3"}},
	}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCompacted})
	roots := Build([]adapter.Session{
		sess("new", "t1", "seed", "t3"),
		sess("old", "t1", "t2", "t3"),
		sess("branch", "t1", "t3", "t5"),
	}, st)

	byID := map[string]*Node{}
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "branch" {
			byID[n.Node.ID] = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}

	t1 := byID["t1"]
	if t1 == nil || !t1.Superseded {
		t.Fatalf("branch's t1 must dedupe against the replacement's t1, got %+v", t1)
	}
	t3 := byID["t3"]
	if t3 == nil || !t3.Superseded {
		t.Fatalf("branch's t3 (the graft point) must dedupe against the replacement's t3, got %+v", t3)
	}
	t5 := byID["t5"]
	if t5 == nil || !t5.Grafted || !t5.IsSessionRoot {
		t.Fatalf("t5 (new) must be the branch's rendered start: %+v", t5)
	}
}

func TestTheCutMarkerSitsWhereTheCutWas(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut, Cut: &store.Cut{Turns: 2, At: "t4"}})
	st.Replace("old2", "new2", store.Branch{Kind: store.KindCut, Cut: &store.Cut{Turns: 3}})
	roots := Build([]adapter.Session{sess("new", "t1", "t4"), sess("new2", "u1", "u2")}, st)
	var here, after int
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "new" && n.Node.ID == "t4" {
			here = n.CutHere
		}
		if n.SessionID == "new2" && n.IsSessionLeaf {
			after = n.CutAfter
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	if here != 2 || after != 3 {
		t.Fatalf("CutHere %d on t4, CutAfter %d on the leaf; want 2 and 3", here, after)
	}
}
