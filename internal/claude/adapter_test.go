package claude

import (
	"os"
	"path/filepath"
	"strings"
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

func TestAdapterReadsViaTheDiscoveredPath(t *testing.T) {
	// The transcript sits in a directory whose name does not match the
	// session's cwd, exactly as a relocated session does. Branch and Preview
	// must still find it.
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	id := "33333333-3333-4333-8333-333333333333"

	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(projects, "-relocated-elsewhere")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(odd, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if _, ok := e.Raw["cwd"]; ok {
			e.Raw["cwd"] = repoDir
		}
		e.Raw["sessionId"] = id
		b, _ := Marshal(e)
		f.Write(append(b, '\n'))
	}
	f.Close()

	sessions, err := New().Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions %d want 1", len(sessions))
	}
	if _, _, _, err := New().Preview(sessions[0], sessions[0].Nodes[1].ID); err != nil {
		t.Fatalf("Preview could not read a relocated session: %v", err)
	}
	if _, err := New().Branch(sessions[0], sessions[0].Nodes[1].ID, repoDir); err != nil {
		t.Fatalf("Branch could not read a relocated session: %v", err)
	}
}

func TestAgentNameIsUniquePerPane(t *testing.T) {
	sid := "60c5b417-ec35-4ea6-93bb-8246b877b19f"
	a := agentName(sid, "wA:p2")
	b := agentName(sid, "wA:p3")
	if a == b {
		t.Fatalf("the same session in two panes produced the same name %q; herdr requires live agent names to be unique", a)
	}
	for _, n := range []string{a, b} {
		if !strings.HasPrefix(n, "tree-") {
			t.Fatalf("name %q must start with tree-", n)
		}
		if len(n) > 32 {
			t.Fatalf("name %q is longer than herdr allows", n)
		}
		for _, r := range n {
			ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
			if !ok {
				t.Fatalf("name %q contains %q, outside [a-z0-9_-]", n, r)
			}
		}
		if n[0] < 'a' || n[0] > 'z' {
			t.Fatalf("name %q must start with a letter", n)
		}
	}
}

func TestPreviewRejectsAnUnknownNode(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, _ := New().Discover(repoDir)
	if _, _, _, err := New().Preview(sessions[0], "no-such-node"); err == nil {
		t.Fatal("want an error for a node that is not in the transcript")
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
