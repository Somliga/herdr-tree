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

func TestSelectKeepsEveryToolResultOfAParallelCall(t *testing.T) {
	es, _, err := ParseFile("testdata/parallel.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Select(es, "u2")
	if err != nil {
		t.Fatal(err)
	}
	// Chain: u2 <- a3 <- tr1 <- a1 <- u1. a2 arrives by requestId r1. tr2 is a
	// SIBLING result, reachable only by rule 4 — and a2's tool_use t2 is
	// meaningless without it.
	want := []string{"a1", "a2", "a3", "tr1", "tr2", "u1", "u2"}
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

func TestGraftLeavesNoToolUseWithoutItsResult(t *testing.T) {
	// The invariant that actually matters: every tool_use kept must have its
	// tool_result kept too. Claude Code's own transcripts satisfy this; a
	// graft that breaks it writes a conversation shape that cannot exist.
	for _, fixture := range []string{"testdata/parallel.jsonl", "testdata/simple.jsonl"} {
		es, _, err := ParseFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if !IsPrompt(e) {
				continue
			}
			keep, err := Select(es, e.UUID())
			if err != nil {
				t.Fatal(err)
			}
			uses, results := map[string]bool{}, map[string]bool{}
			for _, k := range es {
				if k.UUID() == "" || !keep[k.UUID()] {
					continue
				}
				m, _ := k.Raw["message"].(map[string]any)
				if m == nil {
					continue
				}
				blocks, _ := m["content"].([]any)
				for _, b := range blocks {
					blk, ok := b.(map[string]any)
					if !ok {
						continue
					}
					if blk["type"] == "tool_use" {
						if id, ok := blk["id"].(string); ok {
							uses[id] = true
						}
					}
					if blk["type"] == "tool_result" {
						if id, ok := blk["tool_use_id"].(string); ok {
							results[id] = true
						}
					}
				}
			}
			for id := range uses {
				if !results[id] {
					t.Fatalf("%s: grafting at %s orphaned tool_use %q", fixture, e.UUID(), id)
				}
			}
		}
	}
}

func TestSelectDoesNotKeepWorkFromAfterTheGraftPoint(t *testing.T) {
	// An assistant turn AFTER the branch point shares nothing with the chain,
	// but an unbounded requestId sweep can still reach it. Nothing authored
	// after the branch belongs in the graft.
	es, _, err := ParseFile("testdata/parallel.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Append a later turn whose response reuses requestId r1.
	later := []string{
		`{"type":"user","uuid":"u3","parentUuid":"u2","sessionId":"S","timestamp":"2026-01-01T10:00:07Z","message":{"role":"user","content":[{"type":"text","text":"third"}]}}`,
		`{"type":"assistant","uuid":"a9","parentUuid":"u3","sessionId":"S","requestId":"r1","timestamp":"2026-01-01T10:00:08Z","message":{"role":"assistant","content":[{"type":"text","text":"later work"}]}}`,
	}
	for _, l := range later {
		e, err := parseLine([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}

	keep, err := Select(es, "u2")
	if err != nil {
		t.Fatal(err)
	}
	if keep["a9"] {
		t.Fatal("kept an assistant entry written after the graft point: that is work the user branched away from")
	}
	if keep["u3"] {
		t.Fatal("kept a prompt from after the graft point")
	}
	if !keep["a2"] {
		t.Fatal("the bound must not drop the legitimate same-turn sibling a2")
	}
}

func TestSelectUnknownNode(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	if _, err := Select(es, "nope"); err != ErrNodeNotFound {
		t.Fatalf("got %v want ErrNodeNotFound", err)
	}
}
