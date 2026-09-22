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
	for _, c := range []struct {
		n     *tree.Node
		glyph string
	}{
		{&tree.Node{Node: adapter.Node{Kind: adapter.KindSummaryImport, Title: "x"}}, "⤶"},
		{&tree.Node{Node: adapter.Node{Kind: adapter.KindSummaryCompaction, Title: "x"}}, "⤶"},
		{&tree.Node{Broken: true, IsSessionRoot: true, SessionID: "s"}, "⚠"},
	} {
		text, _ := renderRow(Row{Node: c.n}, false, "", 80)
		if !strings.Contains(text, c.glyph) {
			t.Fatalf("row %q lacks its glyph %q — colour must not carry it alone", text, c.glyph)
		}
	}
}
