package tui

import (
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

func TestStyleForEachKind(t *testing.T) {
	cases := []struct {
		kind adapter.Kind
		want StyleKey
	}{
		{adapter.KindHuman, StyleHuman},
		{adapter.KindAssistant, StyleAssistant},
		{adapter.KindToolCall, StyleTool},
		{adapter.KindSummaryImport, StyleImport},
		{adapter.KindSummaryCompaction, StyleCompaction},
	}
	for _, c := range cases {
		n := &tree.Node{Node: adapter.Node{Kind: c.kind}}
		if got := styleFor(n, false); got != c.want {
			t.Fatalf("kind %v styled %v want %v", c.kind, got, c.want)
		}
	}
	if styleFor(&tree.Node{Broken: true}, false) != StyleBroken {
		t.Fatal("a broken session must be styled as broken whatever its kind")
	}
}

func TestRenderRowStaysUnstyled(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "hello"}, SessionID: "s"}
	text, _ := renderRow(Row{Node: n}, false, "", 80)
	if strings.ContainsRune(text, '\x1b') {
		t.Fatalf("renderRow returned escape sequences: %q", text)
	}
	if !strings.Contains(text, "hello") {
		t.Fatalf("row lost its content: %q", text)
	}
}

func TestEveryColouredDistinctionAlsoHasAGlyph(t *testing.T) {
	// Colour is lost on copy, in logs, and against a clashing theme.
	//
	// The summary titles here are realistic on purpose. The ⤶ and the word
	// that follows it come from the entry's own first line — Classify only
	// assigns these kinds when that prefix is present — so a synthetic title
	// like "x" would test a carrier that does not exist in any real row.
	for _, c := range []struct {
		n     *tree.Node
		glyph string
	}{
		{&tree.Node{Node: adapter.Node{Kind: adapter.KindSummaryImport, Title: "⤶ summary of f2af34a4"}}, "⤶ summary of"},
		{&tree.Node{Node: adapter.Node{Kind: adapter.KindSummaryCompaction, Title: "⤶ compacted t3..t9"}}, "⤶ compacted"},
		{&tree.Node{Broken: true, IsSessionRoot: true, SessionID: "s"}, "⚠"},
	} {
		text, _ := renderRow(Row{Node: c.n}, false, "", 80)
		if !strings.Contains(text, c.glyph) {
			t.Fatalf("row %q lacks its glyph %q — colour must not carry it alone", text, c.glyph)
		}
	}
}

// Spec §6b gives the current session's tip its own colour and its own ● glyph.
// Nothing consumed StyleCurrent until this test: an earlier draft styled the
// tip as an ordinary prompt and spent the green on nothing.
func TestCurrentTipIsStyledAndMarked(t *testing.T) {
	tip := &tree.Node{
		Node: adapter.Node{ID: "n9", Title: "last thing"}, SessionID: "sid-a",
		IsSessionLeaf: true,
	}
	text, key := renderRow(Row{Node: tip}, false, "sid-a", 80)
	if key != StyleCurrent {
		t.Fatalf("the tip of the session you are in is styled %v, want StyleCurrent", key)
	}
	if !strings.Contains(text, "● current") {
		t.Fatalf("colour never carries alone; the tip needs its glyph too: %q", text)
	}

	// Another session's leaf is not your tip.
	_, key = renderRow(Row{Node: tip}, false, "sid-b", 80)
	if key == StyleCurrent {
		t.Fatal("a leaf in another session must not be styled as the current tip")
	}
	// A broken tip is broken first: an unreadable transcript outranks it.
	broken := &tree.Node{Node: adapter.Node{ID: "n9"}, SessionID: "sid-a", IsSessionLeaf: true, Broken: true}
	if _, key := renderRow(Row{Node: broken}, false, "sid-a", 80); key != StyleBroken {
		t.Fatalf("a broken tip styled %v, want StyleBroken", key)
	}
}

// Spec §6b: colour reinforces, it never carries alone. A range that is only
// a colour is a defect — the ┃ gutter is the range's non-colour carrier.
func TestRangeRowIsStyledAndMarked(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Kind: adapter.KindAssistant, Title: "reply"}, SessionID: "s"}
	text, key := renderRow(Row{Node: n, InRange: true}, false, "", 80)
	if key != StyleRange {
		t.Fatalf("in-range row styled %v, want StyleRange", key)
	}
	if !strings.Contains(text, "┃") {
		t.Fatalf("in-range row lacks its glyph: %q", text)
	}

	// A broken session outranks range colouring — data integrity over an
	// in-progress selection — but must still keep the range's own glyph, so
	// the row does not silently drop out of the range visually.
	broken := &tree.Node{SessionID: "s", IsSessionRoot: true, Broken: true}
	text, key = renderRow(Row{Node: broken, InRange: true}, false, "", 80)
	if key != StyleBroken {
		t.Fatalf("a broken row in range styled %v, want StyleBroken", key)
	}
	if !strings.Contains(text, "┃") {
		t.Fatalf("broken-but-in-range row lost its range glyph: %q", text)
	}

	// A row outside the range gets neither.
	out, key := renderRow(Row{Node: n}, false, "", 80)
	if key == StyleRange || strings.Contains(out, "┃") {
		t.Fatalf("a row outside the range must not carry the range marker: %q key=%v", out, key)
	}
}

// The ⤶ comes from the entry's own text, so the renderer must not add a
// second one. Synthetic titles in other tests never collide with the real
// prefix, which is how "⤶ ⤶ summary of …" reached a real screen.
func TestSummaryRowCarriesExactlyOneMarker(t *testing.T) {
	for _, kind := range []adapter.Kind{adapter.KindSummaryImport, adapter.KindSummaryCompaction} {
		n := &tree.Node{
			Node:      adapter.Node{ID: "n1", Kind: kind, Title: "⤶ summary of f2af34a4 — redis-backed sessions"},
			SessionID: "s",
		}
		text, _ := renderRow(Row{Node: n}, false, "", 120)
		if got := strings.Count(text, "⤶"); got != 1 {
			t.Fatalf("kind %v rendered %d markers, want 1: %q", kind, got, text)
		}
	}
}

// A range takes the colour slot only from rows whose colour is decorative.
// The two summary colours are the sole at-a-glance difference between
// "knowledge arrived here" and "this line contracted", so a range sweeping
// over an earlier summary must not flatten them — the ┃ already says ranged.
func TestARangeDoesNotTakeTheColourOfARowThatNeedsIt(t *testing.T) {
	for _, c := range []struct {
		kind adapter.Kind
		want StyleKey
	}{
		{adapter.KindSummaryImport, StyleImport},
		{adapter.KindSummaryCompaction, StyleCompaction},
		{adapter.KindHuman, StyleRange},
		{adapter.KindAssistant, StyleRange},
	} {
		n := &tree.Node{Node: adapter.Node{ID: "n1", Kind: c.kind, Title: "⤶ summary of abc"}, SessionID: "s"}
		text, key := renderRow(Row{Node: n, InRange: true}, false, "", 120)
		if key != c.want {
			t.Fatalf("kind %v in a range styled %v, want %v", c.kind, key, c.want)
		}
		if !strings.Contains(text, "┃") {
			t.Fatalf("kind %v lost the range glyph: %q", c.kind, text)
		}
	}
	broken := &tree.Node{Node: adapter.Node{ID: "n1"}, SessionID: "s", Broken: true}
	if _, key := renderRow(Row{Node: broken, InRange: true}, false, "", 120); key != StyleBroken {
		t.Fatalf("broken in a range styled %v, want StyleBroken", key)
	}
}
