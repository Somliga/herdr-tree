package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/claude"
	"herdr-tree/internal/herdr"
	"herdr-tree/internal/tui"
)

// internal/tui must not import internal/claude, so it carries its own copy of
// the two seed markers. This is the only place that imports both, and the
// copies have to agree exactly: GraftSeeded refuses a seed that does not begin
// with one of them at byte zero, and Classify picks import versus compaction
// by the same strings.
func TestSeedPrefixesAgreeAcrossThePackageBoundary(t *testing.T) {
	if tui.SummaryPrefix != claude.SummaryPrefix {
		t.Fatalf("summary prefix: tui has %q, claude has %q", tui.SummaryPrefix, claude.SummaryPrefix)
	}
	if tui.CompactionPrefix != claude.CompactionPrefix {
		t.Fatalf("compaction prefix: tui has %q, claude has %q", tui.CompactionPrefix, claude.CompactionPrefix)
	}
}

// fakeCurrent stands in for the adapter's inference. It must not be reached
// when the opener told us which session this is.
type fakeCurrent struct {
	adapter.Adapter
	sid    string
	err    error
	called bool
}

func (f *fakeCurrent) Current(adapter.Pane) (string, error) {
	f.called = true
	return f.sid, f.err
}

func TestCurrentSessionPrefersWhatTheOpenerTold(t *testing.T) {
	// herdr answers, and the adapter would infer a DIFFERENT session: an
	// unavailable herdr would make this pass whichever order the two are
	// tried in.
	stubHerdr(t, `echo '{"result":{"pane":{"pane_id":"wA:p1","cwd":"/repo"}}}'`)

	a := &fakeCurrent{sid: "inferred-sid"}
	t.Setenv(herdr.SessionEnv, "told-sid")
	if got := currentSession(a); got != "told-sid" {
		t.Fatalf("got %q, want the session the opener passed", got)
	}
	if a.called {
		t.Fatal("the adapter was asked to infer a session we were already told")
	}
}

func TestCurrentSessionFallsBackToInference(t *testing.T) {
	// A hand-launched overlay has no opener to have set anything, and the
	// inference must still run — deleting it would break that case.
	t.Setenv(herdr.SessionEnv, "")
	stubHerdr(t, `echo '{"result":{"pane":{"pane_id":"wA:p1","cwd":"/repo"}}}'`)

	a := &fakeCurrent{sid: "inferred-sid"}
	if got := currentSession(a); got != "inferred-sid" {
		t.Fatalf("got %q, want the inferred session", got)
	}
	if !a.called {
		t.Fatal("the fallback did not run")
	}
}

// stubHerdr puts a fake herdr on HERDR_BIN_PATH. Nothing here may reach the
// real binary: a live Herdr would open panes and talk to running agents.
func stubHerdr(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
	return bin
}

// open() runs in the pane the user is looking at, which is the only place
// that knows which session that pane is running. If it does not pass that on,
// the overlay is back to inferring it from a shared cwd.
func TestOpenTellsTheOverlayWhichSessionThePaneIsRunning(t *testing.T) {
	const sid = "6d6dffb3-1677-4215-888a-819585242bac"
	argvFile := filepath.Join(t.TempDir(), "argv")
	stubHerdr(t, `if [ "$1" = "pane" ]; then
  echo '{"result":{"pane":{"pane_id":"wA:p1","cwd":"`+t.TempDir()+`","agent":"claude","agent_session":{"value":"`+sid+`"}}}}'
  exit 0
fi
printf '%s\n' "$@" > `+argvFile)

	if err := open(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(b)), "\n")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--env "+herdr.SessionEnv+"="+sid) {
		t.Fatalf("open() did not pass the pane's session to the overlay: %q", joined)
	}
}
