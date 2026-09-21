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
	// Verified against Claude Code 2.1.278: every non-alphanumeric rune
	// becomes "-", per rune rather than per byte. Writing a graft into the
	// wrong directory produces a session Claude Code can never find.
	cases := []struct{ in, want string }{
		{"/home/a/projects/x", "-home-a-projects-x"},
		{"/home/a/doc writing", "-home-a-doc-writing"},
		{"/home/a/slug_test.dir v2+x", "-home-a-slug-test-dir-v2-x"},
		{"/home/a/Solör Bioenergi", "-home-a-Sol-r-Bioenergi"},
		{"/home/a/keeps-dashes", "-home-a-keeps-dashes"},
	}
	for _, c := range cases {
		if got := SlugFor(c.in); got != c.want {
			t.Fatalf("SlugFor(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestDiscoverCarriesTheDiscoveredPath(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	id := "11111111-1111-4111-8111-111111111111"
	writeSession(t, projects, id, repoDir)

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(projects, SlugFor(repoDir), id+".jsonl")
	if got[0].Path != want {
		t.Fatalf("Path = %q want %q", got[0].Path, want)
	}
}

func TestDiscoverFindsASessionWhoseCWDDoesNotMatchItsDirectory(t *testing.T) {
	// A session that relocated into a worktree keeps its original cwd while
	// its transcript lives under a differently named project directory.
	// Reconstructing the path from the cwd would miss it entirely.
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	id := "22222222-2222-4222-8222-222222222222"

	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(projects, "-some-unrelated-worktree-name")
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

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions %d want 1", len(got))
	}
	if got[0].Path != filepath.Join(odd, id+".jsonl") {
		t.Fatalf("Path = %q; must be where the file WAS FOUND, not where its cwd implies", got[0].Path)
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

func TestDiscoverSkipsOtherReposWithoutFullyParsingThem(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	mine := t.TempDir()
	theirs := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", mine)
	writeSession(t, projects, "22222222-2222-4222-8222-222222222222", theirs)

	// Corrupt the OTHER repo's transcript beyond the head. A full parse would
	// still succeed, but the cheap head check must reject it before we get
	// there — and the result must be identical either way.
	other := filepath.Join(projects, SlugFor(theirs), "22222222-2222-4222-8222-222222222222.jsonl")
	b, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, append(b, []byte("not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CWD != mine {
		t.Fatalf("got %d sessions, want only the one in this repo: %+v", len(got), got)
	}
	if got[0].Broken {
		t.Fatal("our own clean session must not be marked Broken")
	}
}

func TestDiscoverMarksPartialTranscriptBroken(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()

	id := "11111111-1111-4111-8111-111111111111"
	writeSession(t, projects, id, repoDir)

	// Append a line truncated mid-write, exactly as a transcript being
	// appended to right now would look.
	p := filepath.Join(projects, SlugFor(repoDir), id+".jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","uuid":` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions %d want 1", len(got))
	}
	if !got[0].Broken {
		t.Fatal("a transcript with unparseable lines must be Broken, so the tree shows the warning row instead of rendering a partial conversation as complete")
	}
}

func TestDiscoverLeavesCleanSessionUnbroken(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Broken {
		t.Fatalf("clean session must not be Broken: %+v", got)
	}
}

func TestDiscoverExcludesWhollyUnreadableSession(t *testing.T) {
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
