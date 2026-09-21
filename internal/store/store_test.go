package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 || s.RepoRoot != "/repo" || len(s.Branches) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Add("child-sid", Branch{
		GraftedFrom: From{SessionID: "parent-sid", Node: "u3"},
		Title:       "Session-based auth",
		CreatedAt:   time.Date(2026, 9, 21, 17, 40, 0, 0, time.UTC),
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	b, ok := again.Branches["child-sid"]
	if !ok {
		t.Fatal("branch not persisted")
	}
	if b.GraftedFrom.Node != "u3" || b.GraftedFrom.SessionID != "parent-sid" {
		t.Fatalf("got %+v", b)
	}
	if b.Artifacts == nil {
		t.Fatal("artifacts must serialize as [] not null")
	}
}

func TestDifferentReposDoNotShareAFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	a, _ := Load("/repo/a")
	a.Add("s1", Branch{})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := Load("/repo/b")
	if len(b.Branches) != 0 {
		t.Fatal("repos share state")
	}
}

func TestSaveRefusesAStoreThatDidNotComeFromLoad(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s := &Store{Version: 1, RepoRoot: "/repo", Branches: map[string]Branch{}}
	if err := s.Save(); err != ErrNoPath {
		t.Fatalf("got %v want ErrNoPath — otherwise Save writes tree.json into the process cwd", err)
	}
	if _, err := os.Stat("tree.json"); err == nil {
		os.Remove("tree.json")
		t.Fatal("Save wrote tree.json into the working directory")
	}
}

func TestConcurrentSavesKeepBothBranches(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	// Two panes each Load the same store...
	paneA, _ := Load("/repo")
	paneB, _ := Load("/repo")

	// ...each branches from a different turn...
	paneA.Add("session-a", Branch{GraftedFrom: From{SessionID: "src", Node: "u1"}})
	paneB.Add("session-b", Branch{GraftedFrom: From{SessionID: "src", Node: "u3"}})

	// ...and both save.
	if err := paneA.Save(); err != nil {
		t.Fatal(err)
	}
	if err := paneB.Save(); err != nil {
		t.Fatal(err)
	}

	final, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := final.Branches["session-a"]; !ok {
		t.Fatal("pane A's branch was silently discarded by pane B's save")
	}
	if _, ok := final.Branches["session-b"]; !ok {
		t.Fatal("pane B's branch is missing")
	}
}

func TestSuccessiveCorruptionsAreBothPreserved(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	s, _ := Load("/repo")
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(s.path, []byte("{{{ not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load("/repo"); err != nil {
			t.Fatal(err)
		}
	}
	m, err := filepath.Glob(filepath.Join(dir, "tree.json.corrupt.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d corrupt backups want 2 — a second corruption must not overwrite the first", len(m))
	}
}

func TestCorruptStoreIsBackedUpNotFatal(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	s, _ := Load("/repo")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("{{{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatalf("corrupt store must not be fatal: %v", err)
	}
	if len(again.Branches) != 0 {
		t.Fatal("want empty store")
	}
	m, err := filepath.Glob(s.path + ".corrupt.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatal("corrupt file was not preserved as a backup")
	}
}
