package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
)

func TestGraftSeededAppendsTheSeedAsTheLastTurn(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	dst := t.TempDir()

	before, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	seed := SummaryPrefix + " abc123\n\nThe branch concluded X."
	sid, path, err := GraftSeeded("testdata/simple.jsonl", "u3", dst, seed)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile("testdata/simple.jsonl")
	if string(before) != string(after) {
		t.Fatal("seeding modified the source transcript")
	}

	es, skipped, err := ParseFile(path)
	if err != nil || skipped > 0 {
		t.Fatalf("grafted file does not parse: %v skipped=%d", err, skipped)
	}
	var last Entry
	for _, e := range es {
		if e.UUID() != "" {
			last = e
		}
	}
	if last.Text() != seed {
		t.Fatalf("the seed is not the last entry; got %q", last.Text())
	}
	if last.ParentUUID() != "u3" {
		t.Fatalf("seed parented to %q, want u3", last.ParentUUID())
	}
	if last.SessionID() != sid {
		t.Fatalf("seed carries session %q, want %q", last.SessionID(), sid)
	}
	// The leaf pointer is what makes the seed the turn Claude Code resumes at.
	// Without it the summary is written, and never read.
	var leaf string
	for _, e := range es {
		if e.Type() == "last-prompt" {
			if lu, ok := e.Raw["leafUuid"].(string); ok {
				leaf = lu
			}
		}
	}
	if leaf != last.UUID() {
		t.Fatalf("last-prompt points at %q, want the seed %q", leaf, last.UUID())
	}
	k, keep := Classify(last, false)
	if !keep || k != adapter.KindSummaryImport {
		t.Fatalf("seed classified as %v keep=%v", k, keep)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v want 0600", fi.Mode().Perm())
	}
}

func TestGraftWithNoSeedIsUnchanged(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	a, _, err := GraftSeeded("testdata/simple.jsonl", "u3", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if a == "" {
		t.Fatal("want a session id")
	}
	// an empty seed must produce exactly what Graft produces
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	_, pb, err := Graft("testdata/simple.jsonl", "u3", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	esA, _, _ := ParseFile(pb)
	if len(esA) == 0 {
		t.Fatal("control graft produced nothing")
	}
}

func TestGraftWritesResumableSession(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	dstCWD := t.TempDir()

	srcBefore, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	sid, dst, err := Graft("testdata/simple.jsonl", "u3", dstCWD)
	if err != nil {
		t.Fatal(err)
	}
	if sid == "" {
		t.Fatal("no session id returned")
	}
	if want := filepath.Join(projects, SlugFor(dstCWD), sid+".jsonl"); dst != want {
		t.Fatalf("dst %q want %q", dst, want)
	}

	srcAfter, _ := os.ReadFile("testdata/simple.jsonl")
	if string(srcBefore) != string(srcAfter) {
		t.Fatal("source transcript was modified")
	}

	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v want 0600 — conversation content must not be world readable", fi.Mode().Perm())
	}

	es, _, err := ParseFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var last Entry
	seen := map[string]bool{}
	for _, e := range es {
		if u := e.UUID(); u != "" {
			seen[u] = true
		}
		if e.SessionID() != "" && e.SessionID() != sid {
			t.Fatalf("entry kept old sessionId %q", e.SessionID())
		}
		if c := e.CWD(); c != "" && c != dstCWD {
			t.Fatalf("entry kept old cwd %q", c)
		}
		last = e
	}
	for _, want := range []string{"u1", "a1", "a2", "at1", "tr1", "u2", "u3"} {
		if !seen[want] {
			t.Fatalf("missing kept entry %s", want)
		}
	}
	for _, bad := range []string{"a3", "sc1"} {
		if seen[bad] {
			t.Fatalf("descendant %s leaked into graft", bad)
		}
	}
	if last.Type() != "last-prompt" || last.Raw["leafUuid"] != "u3" {
		t.Fatalf("last entry should be the leaf pointer, got %v", last.Raw)
	}
	for _, e := range es {
		if e.Type() == "ai-title" || e.Type() == "cost-state" {
			t.Fatalf("%s should be dropped", e.Type())
		}
	}
}

func TestGraftRefusesPartialTranscript(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	good, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// append a truncated line, as a transcript being written right now has
	if err := os.WriteFile(src, append(good, []byte(`{"type":"user","uuid":`+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Graft(src, "u3", t.TempDir()); err != ErrPartialTranscript {
		t.Fatalf("got %v want ErrPartialTranscript — a dropped line can break the parent chain", err)
	}
}

func TestGraftDropsSessionScopedBookkeeping(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	good, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Entry types that really occur, each carrying state that belongs to the
	// session being branched FROM.
	extra := strings.Join([]string{
		`{"type":"mode","mode":"bypassPermissions","sessionId":"S"}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"S"}`,
		`{"type":"queue-operation","operation":"add","content":"a queued prompt from the old session","sessionId":"S"}`,
		`{"type":"relocated","relocatedCwd":"/old/worktree","sessionId":"S"}`,
		`{"type":"file-history-snapshot","messageId":"m1","snapshot":{}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(src, append(good, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}

	_, dst, err := Graft(src, "u3", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{
		"bypassPermissions",
		"a queued prompt from the old session",
		"/old/worktree",
		"file-history-snapshot",
	} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("old-session state leaked into the graft: %q", leak)
		}
	}
	es, _, err := ParseFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.UUID() == "" && e.Type() != "last-prompt" {
			t.Fatalf("uuid-less %q entry carried into the new session", e.Type())
		}
	}
}

func TestGraftRefusesVersionMismatchAfterTheFirstEntry(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	good, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's FIRST entry is 2.1.278, so a first-entry-wins check would
	// accept this file. A transcript spanning an upgrade must be refused.
	later := []byte(`{"type":"user","uuid":"u9","parentUuid":"u3","sessionId":"S","cwd":"/repo","version":"99.0.0","message":{"role":"user","content":[{"type":"text","text":"later"}]}}` + "\n")
	if err := os.WriteFile(src, append(good, later...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Graft(src, "u3", t.TempDir()); err != ErrUnsupportedVersion {
		t.Fatalf("got %v want ErrUnsupportedVersion", err)
	}
}

func TestGraftRefusesUnknownFormatVersion(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	body := `{"type":"user","uuid":"u1","parentUuid":null,"sessionId":"S","cwd":"/x","version":"99.0.0","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Graft(src, "u1", t.TempDir()); err != ErrUnsupportedVersion {
		t.Fatalf("got %v want ErrUnsupportedVersion", err)
	}
}

// The seed's own prefix is the single source of truth for what the entry is.
// A hardcoded kind would stamp a compaction entry as an import — false data in
// a file we write into the user's ~/.claude, where nothing would contradict it.
func TestSeedKindIsDerivedFromThePrefix(t *testing.T) {
	for _, c := range []struct {
		seed string
		want any
	}{
		{SummaryPrefix + " abc\n\nx", "summary"},
		{CompactionPrefix + " t3..t9\n\nx", "compaction"},
	} {
		projects := t.TempDir()
		t.Setenv("CLAUDE_PROJECTS_DIR", projects)
		_, path, err := GraftSeeded("testdata/simple.jsonl", "u3", t.TempDir(), c.seed)
		if err != nil {
			t.Fatal(err)
		}
		es, _, err := ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var last Entry
		for _, e := range es {
			if e.UUID() != "" {
				last = e
			}
		}
		got := last.Raw["herdrTree"]
		m, ok := got.(map[string]any)
		if !ok || m["kind"] != c.want {
			t.Fatalf("seed %q got herdrTree %v, want kind %v", c.seed[:12], got, c.want)
		}
	}
}

// An unmarked seed would be written, would drive the resume, and would be
// invisible in the tree: on an origin-stamped transcript Classify takes the
// authoritative path, finds no origin on an entry we wrote, and drops it.
func TestGraftSeededRefusesAnUnmarkedSeed(t *testing.T) {
	for _, seed := range []string{"carry on with the token service", " ", "\n" + SummaryPrefix + " abc"} {
		projects := t.TempDir()
		t.Setenv("CLAUDE_PROJECTS_DIR", projects)
		_, path, err := GraftSeeded("testdata/simple.jsonl", "u3", t.TempDir(), seed)
		if !errors.Is(err, ErrUnmarkedSeed) {
			t.Fatalf("seed %q: got err %v, want ErrUnmarkedSeed", seed, err)
		}
		if path != "" {
			t.Fatalf("seed %q: refused but still wrote %s", seed, path)
		}
		if ents, _ := os.ReadDir(projects); len(ents) != 0 {
			t.Fatalf("seed %q: refused but left %d entries behind", seed, len(ents))
		}
	}
}
