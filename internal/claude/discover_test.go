package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSession copies the fixture into a fake projects tree, rewriting cwd.
// The slug is derived with SlugFor so the file lands exactly where
// TranscriptPath will later look for it.
func writeSession(t *testing.T, projects, id, cwd string) {
	t.Helper()
	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(projects, SlugFor(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range es {
		if _, ok := e.Raw["cwd"]; ok {
			e.Raw["cwd"] = cwd
		}
		e.Raw["sessionId"] = id
		b, _ := Marshal(e)
		f.Write(append(b, '\n'))
	}
}

func TestSlugFor(t *testing.T) {
	if got := SlugFor("/home/a/projects/x"); got != "-home-a-projects-x" {
		t.Fatalf("got %q", got)
	}
}

func TestDiscoverGroupsByRepoRoot(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)

	repoDir := t.TempDir()
	other := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)
	writeSession(t, projects, "22222222-2222-4222-8222-222222222222", other)

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions want 1", len(got))
	}
	s := got[0]
	if s.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("id %q", s.ID)
	}
	if s.CWD != repoDir {
		t.Fatalf("cwd %q want %q", s.CWD, repoDir)
	}
	if len(s.Nodes) != 2 {
		t.Fatalf("nodes %d want 2", len(s.Nodes))
	}
	if s.Title != "Fixture session" {
		t.Fatalf("title %q", s.Title)
	}
}

func TestDiscoverMarksUnreadableSessionBroken(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)
	// a file with no parseable entry at all
	bad := filepath.Join(projects, SlugFor(repoDir), "33333333-3333-4333-8333-333333333333.jsonl")
	if err := os.WriteFile(bad, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("an unreadable session must not join a repo it cannot claim: got %d", len(got))
	}
}
