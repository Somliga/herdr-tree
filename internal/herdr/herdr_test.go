package herdr

import (
	"time"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubHerdr puts a fake `herdr` first on PATH and clears HERDR_BIN_PATH so
// Bin()'s own fallback resolves to it. It exercises the real subprocess path
// through run() — exit code, stderr — without ever touching the real herdr
// binary or a live session, even if a mutation makes run() fall through to
// exec.
func stubHerdr(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_BIN_PATH", "")
}

const paneCurrentJSON = `{"id":"cli:pane:current","result":{"pane":{
"agent":"claude",
"agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"6d6dffb3-1677-4215-888a-819585242bac"},
"cwd":"/home/somliga/projects/herdr-tree",
"pane_id":"wA:p1","tab_id":"wA:t1","workspace_id":"wA"},"type":"pane_current"}}`

const splitJSON = `{"id":"cli:pane:split","result":{"pane":{"pane_id":"wA:p2","cwd":"/repo"}}}`

func TestParsePaneCurrent(t *testing.T) {
	p, err := parsePaneCurrent([]byte(paneCurrentJSON))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "wA:p1" {
		t.Fatalf("id %q", p.ID)
	}
	if p.CWD != "/home/somliga/projects/herdr-tree" {
		t.Fatalf("cwd %q", p.CWD)
	}
	if p.AgentSessionID != "6d6dffb3-1677-4215-888a-819585242bac" {
		t.Fatalf("agent session %q", p.AgentSessionID)
	}
	if p.Agent != "claude" {
		t.Fatalf("agent %q", p.Agent)
	}
}

func TestParsePaneCurrentWithoutAgent(t *testing.T) {
	p, err := parsePaneCurrent([]byte(`{"result":{"pane":{"pane_id":"wA:p1","cwd":"/x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.AgentSessionID != "" {
		t.Fatalf("want empty agent session, got %q", p.AgentSessionID)
	}
}

func TestParseSplit(t *testing.T) {
	id, err := parseSplit([]byte(splitJSON))
	if err != nil {
		t.Fatal(err)
	}
	if id != "wA:p2" {
		t.Fatalf("got %q", id)
	}
}

func TestParseSplitMissingPane(t *testing.T) {
	if _, err := parseSplit([]byte(`{"result":{}}`)); err == nil {
		t.Fatal("want an error when no pane id is returned")
	}
}

func TestArgvOrderingAndSeparator(t *testing.T) {
	// herdr does NOT accept --flag=value (verified against the real binary:
	// it answers "unknown option"), so ordering and the -- separator are the
	// only things standing between data and the flag parser.
	got := strings.Join(splitArgv("/repo"), " ")
	want := "pane split --current --direction right --cwd /repo --no-focus"
	if got != want {
		t.Fatalf("splitArgv:\n got %q\nwant %q", got, want)
	}

	got = strings.Join(agentStartArgv("tree-abc", "wA:p2", "sid-1"), " ")
	want = "agent start tree-abc --kind claude --pane wA:p2 -- --resume sid-1"
	if got != want {
		t.Fatalf("agentStartArgv:\n got %q\nwant %q", got, want)
	}
	// Everything after "--" belongs to claude, not herdr.
	a := agentStartArgv("tree-abc", "wA:p2", "sid-1")
	sep := -1
	for i, v := range a {
		if v == "--" {
			sep = i
		}
	}
	if sep == -1 {
		t.Fatal("no -- separator: claude's flags would be parsed by herdr")
	}
	if a[sep+1] != "--resume" || a[sep+2] != "sid-1" {
		t.Fatalf("resume args are not behind the separator: %v", a)
	}

	got = strings.Join(openTreePaneArgv("/repo", ""), " ")
	want = "plugin pane open --plugin herdr-tree --entrypoint tree --placement overlay --cwd /repo"
	if got != want {
		t.Fatalf("openTreePaneArgv:\n got %q\nwant %q", got, want)
	}
}

func TestRefusesArgumentsThatWouldBeReadAsFlags(t *testing.T) {
	// A directory literally named "-foo" cannot be passed safely, because the
	// --flag=value escape hatch does not exist in this CLI. Refusing beats
	// letting herdr parse it as a flag.
	if _, err := Split("-rf"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("Split: got %v want ErrUnsafeArgument", err)
	}
	if err := OpenTreePane("--placement", ""); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("OpenTreePane: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("-x", "wA:p1", "sid"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart name: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("tree-a", "-p", "sid"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart pane: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("tree-a", "wA:p1", "-resume"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart session: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("tree-a", "wA:p1", ""); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart empty: got %v want ErrUnsafeArgument", err)
	}
}

func TestAgentPromptArgv(t *testing.T) {
	got := strings.Join(agentPromptArgv("tree-60c5b417-wap2", "⤶ summary of abc\n\ntext"), "\u0000")
	want := strings.Join([]string{"agent", "prompt", "tree-60c5b417-wap2", "⤶ summary of abc\n\ntext", "--wait", "--timeout", "120000"}, "\u0000")
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestAgentPromptRefusesFlagShapedAgentName(t *testing.T) {
	if err := AgentPrompt("-x", "hello"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("got %v want ErrUnsafeArgument", err)
	}
}

func TestAgentPromptRefusesEmptyText(t *testing.T) {
	if err := AgentPrompt("tree-a", "   "); err == nil {
		t.Fatal("sending an empty message to an agent is never intended")
	}
}

func TestAgentPromptSurfacesBlockedFromStderr(t *testing.T) {
	// Empirically verified against the real binary: herdr writes its error
	// as JSON on STDERR with a non-zero exit, and stdout is empty. That
	// means AgentPrompt must recover the error body from stderr, not stdout
	// — classifyAgentError(out) alone can never see it.
	stubHerdr(t, `echo '{"error":{"code":"agent_blocked","message":"agent is at an approval dialog"}}' 1>&2
exit 1`)
	if err := AgentPrompt("tree-a", "hello"); !errors.Is(err, ErrAgentBlocked) {
		t.Fatalf("got %v want ErrAgentBlocked", err)
	}
}

func TestRunErrorOmitsArgvContent(t *testing.T) {
	stubHerdr(t, `echo "boom" 1>&2
exit 1`)
	const secret = "text nobody should see in a log"
	_, err := run("agent", "prompt", "tree-a", secret, "--wait", "--timeout", "120000")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("run's error leaked argv content: %v", err)
	}
}

func TestAgentPromptErrorOmitsMessageContent(t *testing.T) {
	stubHerdr(t, `echo '{"error":{"code":"agent_not_found","message":"agent target x not found"}}' 1>&2
exit 1`)
	const secret = "the secret summary text nobody should see in a log"
	err := AgentPrompt("tree-a", secret)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked message content: %v", err)
	}
}

func TestBlockedAgentIsDistinguishable(t *testing.T) {
	err := classifyAgentError([]byte(`{"error":{"code":"agent_blocked","message":"agent is at an approval dialog"}}`))
	if !errors.Is(err, ErrAgentBlocked) {
		t.Fatalf("got %v want ErrAgentBlocked", err)
	}
	if classifyAgentError([]byte(`{"error":{"code":"something_else"}}`)) == nil {
		t.Fatal("a non-blocked error is still an error")
	}
	if classifyAgentError([]byte(`{"result":{}}`)) != nil {
		t.Fatal("a success is not an error")
	}
}

// A message beginning with a dash would be parsed as a flag and quoted back
// in herdr's own stderr, which then becomes our error string. Neither this
// refusal nor anything downstream may repeat the message.
func TestAgentPromptRefusesFlagShapedMessageWithoutEchoingIt(t *testing.T) {
	secret := "-x the user's private conversation text"
	err := AgentPrompt("tree-abc", secret)
	if !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("got %v, want ErrUnsafeArgument", err)
	}
	if strings.Contains(err.Error(), "private conversation") {
		t.Fatalf("the refusal echoed the message: %q", err)
	}
}

// The timeout path formats its own error, so it is its own leak route. No test
// drove run() into it until a review reverted cmdWords there and watched the
// whole suite stay green.
func TestRunTimeoutOmitsArgvContent(t *testing.T) {
	// `exec` matters: CommandContext kills the direct child only, so a stub
	// that FORKS sleep keeps the stdout pipe open and Output() blocks for the
	// full five seconds even though the context fired on time.
	stubHerdr(t, "exec sleep 5")
	old := timeout
	timeout = 20 * time.Millisecond
	t.Cleanup(func() { timeout = old })

	secret := "⤶ summary of abc\n\nthe user's private conversation text"
	err := AgentPrompt("tree-abc", secret)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want a timeout", err)
	}
	if strings.Contains(err.Error(), "private conversation") {
		t.Fatalf("the timeout error leaked the message: %q", err)
	}
}

// realAgentList is herdr's own `agent list` output, captured from a live
// session with four agents running — two of them in this repo.
func realAgentList(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "agent-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseAgentListMapsSessionsToTheirPanes(t *testing.T) {
	got, err := parseAgentList(realAgentList(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d live agents, want the fixture's 4: %v", len(got), got)
	}
	// The plugin's own repo has two, in different panes; resolving to the
	// wrong one sends a summary into the wrong conversation.
	if got["bdf6207b-7062-4f33-93c4-e86a0b3b438d"] != "wA:p1" {
		t.Fatalf("session bdf6207b resolved to %q, want wA:p1", got["bdf6207b-7062-4f33-93c4-e86a0b3b438d"])
	}
	if got["684ae569-5d55-4e07-90a3-341117c819df"] != "wA:pY" {
		t.Fatalf("session 684ae569 resolved to %q, want wA:pY", got["684ae569-5d55-4e07-90a3-341117c819df"])
	}
	// and one in another repo entirely
	if got["448ae587-f274-43c9-ae32-b96f883b5e08"] != "wE:p1" {
		t.Fatalf("session 448ae587 resolved to %q, want wE:p1", got["448ae587-f274-43c9-ae32-b96f883b5e08"])
	}
}

// agent_session.value is the last session id OBSERVED in a pane, and v1 saw a
// headless run overwrite one. Two panes claiming the same session is therefore
// reachable, and there is no right answer: pick one and the summary lands in a
// conversation the user was not looking at.
func TestAnAmbiguousSessionResolvesToNoAgent(t *testing.T) {
	dup := strings.Replace(string(realAgentList(t)),
		"97b3d58d-4888-4dcf-affe-7b12cbf2d246",
		"bdf6207b-7062-4f33-93c4-e86a0b3b438d", 1)

	got, err := parseAgentList([]byte(dup))
	if err != nil {
		t.Fatal(err)
	}
	if target, ok := got["bdf6207b-7062-4f33-93c4-e86a0b3b438d"]; ok {
		t.Fatalf("a session held by two panes resolved to %q", target)
	}
	if got["684ae569-5d55-4e07-90a3-341117c819df"] != "wA:pY" {
		t.Fatal("the unambiguous agents were dropped too")
	}
}

func TestAgentListArgv(t *testing.T) {
	if got := strings.Join(agentListArgv(), " "); got != "agent list" {
		t.Fatalf("argv %q", got)
	}
}

func TestAgentForSessionResolvesAndReportsAbsence(t *testing.T) {
	stubHerdr(t, "cat "+filepath.Join("testdata", "agent-list.json"))

	got, err := AgentForSession("684ae569-5d55-4e07-90a3-341117c819df")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wA:pY" {
		t.Fatalf("target %q, want wA:pY", got)
	}

	_, err = AgentForSession("11111111-2222-3333-4444-555555555555")
	if !errors.Is(err, ErrNoLiveAgent) {
		t.Fatalf("a session nobody is holding: got %v, want ErrNoLiveAgent", err)
	}
}

func TestAgentForSessionRefusesAFlagShapedSessionId(t *testing.T) {
	stubHerdr(t, "echo should-not-run; exit 1")
	if _, err := AgentForSession("-x"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("got %v, want ErrUnsafeArgument", err)
	}
}

// The overlay must be TOLD which session it is for. Inferring it from a
// shared cwd cannot separate two interactive sessions in one repo, and that
// ambiguity reads as "no session", which empties the trunk as well as the
// send target.
func TestOpenTreePaneArgvCarriesTheSession(t *testing.T) {
	got := openTreePaneArgv("/repo", "6d6dffb3-1677-4215-888a-819585242bac")
	joined := strings.Join(got, " ")
	want := "plugin pane open --plugin herdr-tree --entrypoint tree --placement overlay --cwd /repo" +
		" --env HERDR_TREE_SESSION=6d6dffb3-1677-4215-888a-819585242bac"
	if joined != want {
		t.Fatalf("openTreePaneArgv:\n got %q\nwant %q", joined, want)
	}
	// herdr rejects --flag=value, so the flag and its value must be two
	// elements; the "=" inside the value is what makes that easy to get wrong.
	for i, v := range got {
		if v == "--env" {
			if i+1 >= len(got) || got[i+1] != "HERDR_TREE_SESSION=6d6dffb3-1677-4215-888a-819585242bac" {
				t.Fatalf("--env and its assignment are not two elements: %v", got)
			}
		}
	}
	if strings.Contains(joined, "--env=") {
		t.Fatal("herdr does not accept --flag=value")
	}
}

func TestOpenTreePaneArgvDropsAnImplausibleSession(t *testing.T) {
	for _, bad := range []string{"", "not a uuid", "sid\nHOME=/tmp", "a=b", strings.Repeat("a", 65)} {
		if got := strings.Join(openTreePaneArgv("/repo", bad), " "); strings.Contains(got, "--env") {
			t.Fatalf("session %q was passed through: %q", bad, got)
		}
	}
}
