package tree

import (
	"testing"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

func TestTrunkIsTheLineageOfTheCurrentSession(t *testing.T) {
	// root ── mid ── leaf      and a sibling branch off root
	st := &store.Store{Version: 1, Branches: map[string]store.Branch{}}
	st.Add("mid", store.Branch{GraftedFrom: store.From{SessionID: "root", Node: "r2"}})
	st.Add("leaf", store.Branch{GraftedFrom: store.From{SessionID: "mid", Node: "m2"}})
	st.Add("side", store.Branch{GraftedFrom: store.From{SessionID: "root", Node: "r1"}})

	roots := Build([]adapter.Session{
		sess("root", "r1", "r2", "r3"),
		sess("mid", "m1", "m2", "m3"),
		sess("leaf", "l1"),
		sess("side", "s1"),
	}, st)

	got := Trunk(roots, "leaf")
	for _, want := range []string{"leaf", "mid", "root"} {
		if !got[want] {
			t.Fatalf("%s should be on the trunk: %v", want, got)
		}
	}
	if got["side"] {
		t.Fatal("a sibling branch is not on the trunk")
	}
}

func TestTrunkMovesWhenYouMove(t *testing.T) {
	st := &store.Store{Version: 1, Branches: map[string]store.Branch{}}
	st.Add("side", store.Branch{GraftedFrom: store.From{SessionID: "root", Node: "r1"}})
	roots := Build([]adapter.Session{sess("root", "r1", "r2"), sess("side", "s1")}, st)

	if !Trunk(roots, "side")["side"] {
		t.Fatal("opening a branch must make it the trunk")
	}
	if Trunk(roots, "side")["root"] != true {
		t.Fatal("its parent is still on the trunk lineage")
	}
}

func TestNoCurrentSessionMeansNoTrunk(t *testing.T) {
	st := &store.Store{Version: 1, Branches: map[string]store.Branch{}}
	roots := Build([]adapter.Session{sess("root", "r1")}, st)
	if len(Trunk(roots, "")) != 0 {
		t.Fatal("with no current session nothing is the trunk")
	}
}

// A hand-edited store can name a graft cycle. It cannot hang Trunk, but not
// because of the guard in the climb: Build attaches every member of a cycle to
// a parent, so no member is a root, so walk never reaches one and parentOf
// never learns the cycle. The guards stay as defence in depth against a future
// Build that attaches differently; what this pins is the observable result.
func TestTrunkTerminatesOnAGraftCycle(t *testing.T) {
	st := &store.Store{Version: 1, Branches: map[string]store.Branch{}}
	st.Add("a", store.Branch{GraftedFrom: store.From{SessionID: "b", Node: "b1"}})
	st.Add("b", store.Branch{GraftedFrom: store.From{SessionID: "a", Node: "a1"}})
	roots := Build([]adapter.Session{sess("a", "a1"), sess("b", "b1")}, st)

	got := Trunk(roots, "a")
	if len(got) != 1 || !got["a"] {
		t.Fatalf("a cycle leaves the current session alone on its trunk, got %v", got)
	}

	// A self-graft is the degenerate case of the same cycle.
	st2 := &store.Store{Version: 1, Branches: map[string]store.Branch{}}
	st2.Add("solo", store.Branch{GraftedFrom: store.From{SessionID: "solo", Node: "s1"}})
	roots2 := Build([]adapter.Session{sess("solo", "s1")}, st2)
	if got := Trunk(roots2, "solo"); len(got) != 1 || !got["solo"] {
		t.Fatalf("self-graft trunk should be just itself, got %v", got)
	}
}
