package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
)

const seedText = CompactionPrefix + " u2..u3\n\nturns two and three, briefly"

// spliced runs Splice into a fresh projects dir and returns the new
// transcript's entries keyed by uuid, plus its uuids in file order.
func spliced(t *testing.T, src string, e adapter.Edit) (adapter.Spliced, map[string]Entry, []string) {
	t.Helper()
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	before, _ := os.ReadFile(src)
	res, err := Splice(src, e, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(src)
	if string(before) != string(after) {
		t.Fatal("splicing modified the source transcript")
	}
	path := filepath.Join(ProjectsDir(), "*", res.SessionID+".jsonl")
	m, _ := filepath.Glob(path)
	if len(m) != 1 {
		t.Fatalf("want one spliced file, found %v", m)
	}
	fi, _ := os.Stat(m[0])
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("spliced file mode %v, want 0600", fi.Mode().Perm())
	}
	es, skipped, err := ParseFile(m[0])
	if err != nil || skipped > 0 {
		t.Fatalf("spliced file does not parse: %v skipped=%d", err, skipped)
	}
	by := map[string]Entry{}
	var order []string
	for _, e := range es {
		if u := e.UUID(); u != "" {
			if e.SessionID() != res.SessionID {
				t.Fatalf("%s carries session %q, want %q", u, e.SessionID(), res.SessionID)
			}
			by[u] = e
			order = append(order, u)
		}
	}
	return res, by, order
}

func seedOf(t *testing.T, by map[string]Entry) Entry {
	t.Helper()
	for _, e := range by {
		if strings.HasPrefix(e.Text(), "⤶") {
			return e
		}
	}
	t.Fatal("no seed entry in the spliced file")
	return Entry{}
}

func parent(e Entry) string { return e.ParentUUID() }

func TestCutRemovesWholeTurnsAndRejoinsTheRest(t *testing.T) {
	res, by, order := spliced(t, "testdata/splice.jsonl", adapter.Edit{From: "a2b", To: "a3"})
	want := []string{"pre1", "u1", "a1", "u4", "a4"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("kept %v, want %v", order, want)
	}
	if parent(by["u4"]) != "a1" {
		t.Fatalf("u4 rejoined to %q, want a1", parent(by["u4"]))
	}
	if res.Removed != 2 || res.After != "u4" {
		t.Fatalf("result %+v, want Removed 2, After u4", res)
	}
}

func TestCompactPutsTheSeedWhereTheRangeWas(t *testing.T) {
	_, by, order := spliced(t, "testdata/splice.jsonl", adapter.Edit{From: "u2", To: "u3", Seed: seedText})
	seed := seedOf(t, by)
	if parent(seed) != "a1" {
		t.Fatalf("seed parented to %q, want a1", parent(seed))
	}
	if parent(by["u4"]) != seed.UUID() {
		t.Fatalf("u4 rejoined to %q, want the seed", parent(by["u4"]))
	}
	for _, gone := range []string{"u2", "a2", "a2b", "tr1", "tr2", "at2", "a2c", "u3", "a3"} {
		if _, ok := by[gone]; ok {
			t.Fatalf("%s survived the compaction", gone)
		}
	}
	// File order puts the seed immediately before what follows it.
	for i, u := range order {
		if u == "u4" && order[i-1] != seed.UUID() {
			t.Fatalf("seed not written just before u4: %v", order)
		}
	}
}

func TestInsertKeepsEverythingAfter(t *testing.T) {
	seed := SummaryPrefix + " other\n\nwhat the branch found"
	res, by, _ := spliced(t, "testdata/splice.jsonl", adapter.Edit{After: "a1", Seed: seed})
	s := seedOf(t, by)
	if parent(s) != "a1" || parent(by["u2"]) != s.UUID() {
		t.Fatalf("seed under %q, u2 under %q; want a1 and the seed", parent(s), parent(by["u2"]))
	}
	for _, kept := range []string{"u2", "a2", "a2b", "tr1", "tr2", "at2", "a2c", "u3", "a3", "u4", "a4"} {
		if _, ok := by[kept]; !ok {
			t.Fatalf("%s was lost by an insert", kept)
		}
	}
	if res.Removed != 0 {
		t.Fatalf("an insert removed %d turns", res.Removed)
	}
}

// Inserting "after" a mid-turn entry lands after the WHOLE turn: anything
// else would separate a tool call from its result.
func TestInsertAfterAToolCallLandsAfterItsTurn(t *testing.T) {
	_, by, _ := spliced(t, "testdata/splice.jsonl", adapter.Edit{After: "a2b", Seed: seedText})
	s := seedOf(t, by)
	if parent(s) != "a2c" || parent(by["u3"]) != s.UUID() {
		t.Fatalf("seed under %q, u3 under %q; want a2c and the seed", parent(s), parent(by["u3"]))
	}
}

func TestCompactingTheFirstTurnKeepsThePreamble(t *testing.T) {
	_, by, _ := spliced(t, "testdata/splice.jsonl", adapter.Edit{From: "u1", To: "a1", Seed: seedText})
	if parent(seedOf(t, by)) != "pre1" {
		t.Fatalf("seed parented to %q, want the preamble's pre1", parent(seedOf(t, by)))
	}
}

func TestWithNoPreambleTheSeedBecomesTheRoot(t *testing.T) {
	_, by, _ := spliced(t, "testdata/parallel.jsonl", adapter.Edit{From: "u1", To: "a3", Seed: seedText})
	s := seedOf(t, by)
	if s.Raw["parentUuid"] != nil {
		t.Fatalf("seed parent %v, want null", s.Raw["parentUuid"])
	}
	if parent(by["u2"]) != s.UUID() {
		t.Fatal("u2 not rejoined to the root seed")
	}
}

func TestACutFromTheFirstTurnMakesTheNextTurnTheRoot(t *testing.T) {
	_, by, _ := spliced(t, "testdata/parallel.jsonl", adapter.Edit{From: "u1", To: "a1"})
	if by["u2"].Raw["parentUuid"] != nil {
		t.Fatalf("u2 parent %v, want null", by["u2"].Raw["parentUuid"])
	}
}

func TestCompactingEverythingLeavesOnlyTheSeed(t *testing.T) {
	_, by, _ := spliced(t, "testdata/parallel.jsonl", adapter.Edit{From: "u1", To: "u2", Seed: seedText})
	if len(by) != 1 {
		t.Fatalf("kept %d entries, want only the seed", len(by))
	}
}

// The leaf pointer names the tip when something follows the edit, and the
// seed when the edit reached the end.
func TestTheLeafPointerFollowsTheEdit(t *testing.T) {
	for _, c := range []struct {
		e    adapter.Edit
		want func(map[string]Entry) string
	}{
		{adapter.Edit{From: "u2", To: "u3", Seed: seedText}, func(map[string]Entry) string { return "a4" }},
		{adapter.Edit{From: "u3", To: "a4", Seed: seedText}, func(by map[string]Entry) string { return seedOf(t, by).UUID() }},
	} {
		res, by, _ := spliced(t, "testdata/splice.jsonl", c.e)
		path, _ := filepath.Glob(filepath.Join(ProjectsDir(), "*", res.SessionID+".jsonl"))
		es, _, _ := ParseFile(path[0])
		leaf := ""
		for _, e := range es {
			if e.Type() == "last-prompt" {
				leaf, _ = e.Raw["leafUuid"].(string)
			}
		}
		if leaf != c.want(by) {
			t.Fatalf("%+v: leaf %q, want %q", c.e, leaf, c.want(by))
		}
	}
}

func TestSpliceRefuses(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	for name, c := range map[string]struct {
		e    adapter.Edit
		want error
	}{
		"a cut of every turn": {adapter.Edit{From: "u1", To: "a4"}, ErrNothingLeft},
		"a rewound stretch":   {adapter.Edit{From: "x1", To: "xa1"}, ErrNotOnLine},
		"an unmarked seed":    {adapter.Edit{From: "u2", To: "u3", Seed: "no prefix"}, ErrUnmarkedSeed},
	} {
		if _, err := Splice("testdata/splice.jsonl", c.e, t.TempDir()); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(ProjectsDir(), "*", "*.jsonl")); len(left) != 0 {
		t.Fatalf("a refused splice wrote %v", left)
	}
}

// Splice reads the source when it runs, not when the range was chosen: turns
// the agent added in between are kept, after the edit.
func TestTurnsAddedSinceTheRangeWasChosenAreKept(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	b, _ := os.ReadFile("testdata/splice.jsonl")
	b = append(b, []byte(`{"type":"user","uuid":"u5","parentUuid":"a4","sessionId":"S","timestamp":"2026-01-01T12:01:00Z","message":{"role":"user","content":[{"type":"text","text":"five"}]}}`+"\n"+
		`{"type":"assistant","uuid":"a5","parentUuid":"u5","sessionId":"S","requestId":"r6","timestamp":"2026-01-01T12:01:01Z","message":{"role":"assistant","content":[{"type":"text","text":"reply five"}]}}`+"\n")...)
	if err := os.WriteFile(src, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_, by, _ := spliced(t, src, adapter.Edit{From: "u2", To: "u3"})
	if parent(by["u5"]) != "a4" || parent(by["a5"]) != "u5" {
		t.Fatal("turns appended after the range was chosen were not kept in place")
	}
}

func TestWidenReportsTurnNumbersAndTheReadingEnd(t *testing.T) {
	s, err := Widen("testdata/splice.jsonl", "a3", "a2b")
	if err != nil {
		t.Fatal(err)
	}
	if s.First != 2 || s.Last != 3 || s.End != "a3" {
		t.Fatalf("span %+v, want 2..3 ending a3", s)
	}
}

// compacted.jsonl is a native /compact: the parent chain restarts at a
// compact_boundary (parentUuid null), followed by the isCompactSummary entry,
// which no one typed and so is preamble (§3.4).
const compacted = "testdata/compacted.jsonl"

func TestARangeBeforeANativeCompactIsNotOnTheLine(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	if _, err := Splice(compacted, adapter.Edit{From: "u1", To: "u3"}, t.TempDir()); !errors.Is(err, ErrNotOnLine) {
		t.Fatalf("err %v, want ErrNotOnLine", err)
	}
}

func TestCompactingEveryTurnAfterANativeCompactKeepsTheBoundary(t *testing.T) {
	_, by, order := spliced(t, compacted, adapter.Edit{From: "u3", To: "a4", Seed: seedText})
	seed := seedOf(t, by)
	if want := "cb,cs," + seed.UUID(); strings.Join(order, ",") != want {
		t.Fatalf("kept %v, want %s", order, want)
	}
	if by["cb"].ParentUUID() != "" || parent(by["cs"]) != "cb" || parent(seed) != "cs" {
		t.Fatalf("boundary under %q, summary under %q, seed under %q", parent(by["cb"]), parent(by["cs"]), parent(seed))
	}
}

func TestCuttingATurnAfterANativeCompactKeepsTheBoundaryAsRoot(t *testing.T) {
	_, by, order := spliced(t, compacted, adapter.Edit{From: "u3", To: "a3"})
	if want := "cb,cs,u4,a4"; strings.Join(order, ",") != want {
		t.Fatalf("kept %v, want %s", order, want)
	}
	if by["cb"].ParentUUID() != "" || parent(by["u4"]) != "cs" {
		t.Fatalf("boundary under %q, u4 under %q", parent(by["cb"]), parent(by["u4"]))
	}
}
