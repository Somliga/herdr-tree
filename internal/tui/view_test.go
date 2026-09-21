package tui

import (
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

func TestRenderRowShowsTitleAndIndent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n2", Title: "i want to discuss the weather"}, SessionID: "82cb69f2-x"}
	got := renderRow(Row{Node: n, Depth: 1}, false, "", 80)
	if !strings.Contains(got, "i want to discuss the weather") {
		t.Fatalf("title missing: %q", got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("depth not indented: %q", got)
	}
}

func TestRenderRowMarksCurrent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	got := renderRow(Row{Node: n}, false, "sid-a", 80)
	if !strings.Contains(got, "● current") {
		t.Fatalf("current marker missing: %q", got)
	}
}

func TestRenderRowShowsSessionIdOnRoots(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "n1", Title: "x"},
		SessionID: "82cb69f2-e18b-4f86-874a-89e93139324a", IsSessionRoot: true,
	}
	got := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "82cb69f2") {
		t.Fatalf("short session id missing: %q", got)
	}
	if strings.Contains(got, "e18b") {
		t.Fatalf("full uuid should not be shown: %q", got)
	}
}

func TestRenderRowMarksBroken(t *testing.T) {
	n := &tree.Node{SessionID: "sid", IsSessionRoot: true, Broken: true}
	got := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("broken marker missing: %q", got)
	}
}

func TestRenderRowMarksGraft(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "m1", Title: "alt"},
		SessionID: "f2af34a4-x", IsSessionRoot: true, Grafted: true,
	}
	got := renderRow(Row{Node: n, Depth: 2}, false, "", 80)
	if !strings.Contains(got, "↳") {
		t.Fatalf("graft marker missing: %q", got)
	}
}

func TestConfirmTextNamesWhatIsCarried(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "u3", Title: "what do you think"}}
	got := confirmText(n, 3, 12, 41984, "/home/somliga/projects/surtr")
	for _, want := range []string{"what do you think", "3 turns", "12 entries", "41 KB", "/home/somliga/projects/surtr"} {
		if !strings.Contains(got, want) {
			t.Fatalf("confirm text missing %q:\n%s", want, got)
		}
	}
}
