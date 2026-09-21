package claude

import (
	"sort"
	"testing"
)

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSelectKeepsChainSiblingsAndAttachments(t *testing.T) {
	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Select(es, "u3")
	if err != nil {
		t.Fatal(err)
	}
	// chain u3<-u2<-tr1<-a2<-u1, plus a1 (shares requestId r1 with a2),
	// plus at1 (attachment child of u1).
	want := []string{"a1", "a2", "at1", "tr1", "u1", "u2", "u3"}
	g := keys(got)
	if len(g) != len(want) {
		t.Fatalf("got %v want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v want %v", g, want)
		}
	}
}

func TestSelectExcludesDescendantsAndSidechains(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	got, _ := Select(es, "u3")
	for _, bad := range []string{"a3", "sc1"} {
		if got[bad] {
			t.Fatalf("%s must not be kept when grafting at u3", bad)
		}
	}
}

func TestSelectAtRootKeepsOnlyRootAndItsSiblings(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	got, _ := Select(es, "u1")
	want := []string{"at1", "u1"}
	g := keys(got)
	if len(g) != len(want) || g[0] != want[0] || g[1] != want[1] {
		t.Fatalf("got %v want %v", g, want)
	}
}

func TestSelectUnknownNode(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	if _, err := Select(es, "nope"); err != ErrNodeNotFound {
		t.Fatalf("got %v want ErrNodeNotFound", err)
	}
}
