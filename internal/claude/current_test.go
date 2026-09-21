package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"herdr-tree/internal/adapter"
)

func writeReg(t *testing.T, dir, pid, sid, cwd, kind string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"pid": pid, "sessionId": sid, "cwd": cwd, "kind": kind,
	})
	if err := os.WriteFile(filepath.Join(dir, pid+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentPrefersInteractiveRegistryEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "real-sid", "/repo", "interactive")

	got, err := Current(adapter.Pane{CWD: "/repo", AgentSessionID: "stale-sid"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "real-sid" {
		t.Fatalf("got %q; Herdr's stale value must not win", got)
	}
}

func TestCurrentIgnoresOtherCwds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "other-sid", "/elsewhere", "interactive")

	if _, err := Current(adapter.Pane{CWD: "/repo"}); err == nil {
		t.Fatal("want an error when no session matches the pane")
	}
}

func TestCurrentIgnoresHeadlessSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "headless-sid", "/repo", "print")

	if _, err := Current(adapter.Pane{CWD: "/repo"}); err == nil {
		t.Fatal("a headless session is not the pane's session")
	}
}

func TestAmbiguityResolvedByHerdrHint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "sid-a", "/repo", "interactive")
	writeReg(t, dir, "2", "sid-b", "/repo", "interactive")

	got, err := Current(adapter.Pane{CWD: "/repo", AgentSessionID: "sid-b"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "sid-b" {
		t.Fatalf("got %q want sid-b", got)
	}
}

func TestCurrentRefusesAPaneWithNoCWD(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	// Exactly one session running anywhere: the tempting case to "just pick it".
	writeReg(t, dir, "1", "some-sid", "/somewhere/else", "interactive")

	if _, err := Current(adapter.Pane{CWD: ""}); err != ErrUnknownCWD {
		t.Fatalf("got %v want ErrUnknownCWD — an unknown pane directory must not match every session", err)
	}
}

func TestCurrentMatchesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)

	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Claude recorded the real path; Herdr reports the pane through the link.
	writeReg(t, dir, "1", "the-sid", real, "interactive")

	got, err := Current(adapter.Pane{CWD: link})
	if err != nil {
		t.Fatalf("symlinked pane cwd should still match: %v", err)
	}
	if got != "the-sid" {
		t.Fatalf("got %q want the-sid", got)
	}
}

func TestCurrentToleratesATrailingSlash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	repoDir := t.TempDir()
	writeReg(t, dir, "1", "the-sid", repoDir, "interactive")

	got, err := Current(adapter.Pane{CWD: repoDir + "/"})
	if err != nil {
		t.Fatalf("trailing slash should not break the match: %v", err)
	}
	if got != "the-sid" {
		t.Fatalf("got %q want the-sid", got)
	}
}

func TestAmbiguityWithoutUsableHintIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "sid-a", "/repo", "interactive")
	writeReg(t, dir, "2", "sid-b", "/repo", "interactive")

	if _, err := Current(adapter.Pane{CWD: "/repo", AgentSessionID: "sid-gone"}); err != ErrAmbiguousSession {
		t.Fatalf("got %v want ErrAmbiguousSession", err)
	}
}
