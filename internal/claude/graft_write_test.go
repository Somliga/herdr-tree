package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
