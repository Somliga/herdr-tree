package tree

import (
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

func emptyStore() *store.Store {
	return &store.Store{Version: 1, Branches: map[string]store.Branch{}}
}
