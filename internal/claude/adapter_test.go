package claude

import (
	"os"
	"path/filepath"
	"testing"

	"herdr-tree/internal/adapter"
)

func TestAdapterSatisfiesInterface(t *testing.T) {
	var _ adapter.Adapter = New()
}

func TestAdapterName(t *testing.T) {
	if New().Name() != "claude" {
		t.Fatalf("got %q", New().Name())
	}
}

func TestAdapterBranchWritesNewSession(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, err := New().Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions %d", len(sessions))
	}
	src := sessions[0]

	sid, err := New().Branch(src, src.Nodes[1].ID, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(projects, SlugFor(repoDir), sid+".jsonl")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("grafted session not written: %v", err)
	}
}

func TestAdapterPreviewCountsWhatIsCarried(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, _ := New().Discover(repoDir)
	src := sessions[0]
	turns, entries, size, err := New().Preview(src, src.Nodes[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("turns %d want 2", turns)
	}
	if entries != 7 {
		t.Fatalf("entries %d want 7", entries)
	}
	if size <= 0 {
		t.Fatalf("size %d", size)
	}
}

func TestAdapterBranchRejectsUnknownNode(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, _ := New().Discover(repoDir)
	if _, err := New().Branch(sessions[0], "no-such-node", repoDir); err == nil {
		t.Fatal("want an error")
	}
}
