package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRootOfPlainRepo(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init")
	got, isGit, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !isGit {
		t.Fatal("want isGit true")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWorktreeFoldsIntoMainRepo(t *testing.T) {
	main := t.TempDir()
	git(t, main, "init")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "f")
	git(t, main, "commit", "-m", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, main, "worktree", "add", "-b", "side", wt)

	got, isGit, err := Root(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !isGit {
		t.Fatal("want isGit true")
	}
	want, _ := filepath.EvalSymlinks(main)
	if got != want {
		t.Fatalf("worktree resolved to %q, want main repo %q", got, want)
	}
}

func TestRemovedWorktreeStillResolvesToItsRepo(t *testing.T) {
	main := t.TempDir()
	git(t, main, "init")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "f")
	git(t, main, "commit", "-m", "init")

	// A worktree inside the repo, then deleted — the everyday case.
	wt := filepath.Join(main, ".worktrees", "gone")
	git(t, main, "worktree", "add", "-b", "side", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	got, isGit, err := Root(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !isGit {
		t.Fatal("a removed worktree must still resolve to its repo, or its sessions vanish from the tree")
	}
	want, _ := filepath.EvalSymlinks(main)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRemovedDirOutsideAnyRepoDoesNotMisattribute(t *testing.T) {
	base := t.TempDir()
	gone := filepath.Join(base, "never-existed", "deeper")
	got, isGit, err := Root(gone)
	if err != nil {
		t.Fatal(err)
	}
	if isGit {
		t.Fatalf("walked up into a repo that never contained %q: got %q", gone, got)
	}
}

func TestNonRepoReturnsDirItself(t *testing.T) {
	dir := t.TempDir()
	got, isGit, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if isGit {
		t.Fatal("want isGit false")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
