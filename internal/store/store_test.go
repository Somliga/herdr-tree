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
	if _, err := os.Stat(s.path + ".corrupt"); err != nil {
		t.Fatal("corrupt file was not preserved as a backup")
	}
}
