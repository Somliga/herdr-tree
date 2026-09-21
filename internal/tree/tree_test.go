package tree

import (
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

func TestEmptySessionStillRenders(t *testing.T) {
	roots := Build([]adapter.Session{sess("s1")}, emptyStore())
	if len(roots) != 1 {
		t.Fatalf("roots %d want 1", len(roots))
	}
	if !roots[0].Broken && roots[0].Node.ID != "" {
		t.Fatalf("got %+v", roots[0])
	}
}

func emptyStore() *store.Store {
	return &store.Store{Version: 1, Branches: map[string]store.Branch{}}
}
