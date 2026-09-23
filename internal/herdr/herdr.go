// Package herdr shells out to the herdr CLI. Herdr owns pane and process
// lifecycle; this plugin only asks it for things.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"herdr-tree/internal/adapter"
)

// timeout bounds every herdr invocation. `agent start` waits for the agent to
// become ready (herdr's own default is 30s), and these calls are made from the
// TUI, so an unbounded wait is a permanently stuck overlay with no way out.
// A var, not a const, only so a test can shrink it: the timeout path formats
// an error too, and it leaked the whole argv until a review caught that no
// test ever drove run() into it.
var timeout = 45 * time.Second

// ErrUnsafeArgument means a value would be read as a flag rather than as data.
// herdr does NOT accept the --flag=value form (verified: it answers "unknown
// option"), so a value beginning with "-" cannot be passed safely at all and
// the only correct move is to refuse it.
var ErrUnsafeArgument = errors.New("value would be read as a flag")

// ErrAgentBlocked means the agent is at an approval or question dialog and
// will not accept input. The caller must surface this rather than fall back to
// grafting: the user asked to continue a conversation, not to fork one.
var ErrAgentBlocked = errors.New("agent is waiting for input of its own")

func checkArg(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty: %w", what, ErrUnsafeArgument)
	}
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("%s %q: %w", what, v, ErrUnsafeArgument)
	}
	return nil
}

// Bin is the herdr binary. Herdr sets HERDR_BIN_PATH when it invokes a plugin.
func Bin() string {
	if b := os.Getenv("HERDR_BIN_PATH"); b != "" {
		return b
	}
	return "herdr"
}

// The argv builders are separated from the calls so that flag ordering and
// the placement of "--" are regression-tested without a running herdr.

func splitArgv(cwd string, focus bool) []string {
	argv := []string{"pane", "split", "--current", "--direction", "right", "--cwd", cwd}
	if !focus {
		argv = append(argv, "--no-focus")
	}
	return argv
}

func agentStartArgv(name, paneID, sessionID string) []string {
	return []string{"agent", "start", name, "--kind", "claude", "--pane", paneID, "--", "--resume", sessionID}
}

// isSessionID reports whether s is plausible as a session id, and therefore
// safe to interpolate into an environment assignment. A session id is a uuid;
// a value carrying whitespace, a newline or an "=" is something else, and
// anything reading the environment back as text would misread it. Rejected
// values are dropped rather than refused, so the overlay falls back to
// inferring the session — no worse than before it was told.
func isSessionID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

func openTreePaneArgv(cwd, sessionID string) []string {
	argv := []string{"plugin", "pane", "open",
		"--plugin", "herdr-tree", "--entrypoint", "tree",
		"--placement", "overlay", "--cwd", cwd}
	// Tell the overlay which session it was opened for. The pane running this
	// knows; the overlay's own pane runs no agent, so all it could do is
	// infer from a shared cwd — and two interactive sessions in one repo make
	// that inference ambiguous, which reads as "no session at all".
	//
	// --env and KEY=VALUE are two argv elements: herdr does not accept the
	// --flag=value form. The "=" inside the value is fine, it is one element.
	if isSessionID(sessionID) {
		argv = append(argv, "--env", SessionEnv+"="+sessionID)
	}
	return argv
}

// SessionEnv names the session the overlay was opened for. Exported so
// cmd/herdr-tree reads the same key this sets.
const SessionEnv = "HERDR_TREE_SESSION"

func agentListArgv() []string {
	return []string{"agent", "list"}
}

// agentPromptWait is how long herdr is asked to wait for the agent to settle,
// and agentPromptSlack is how much longer OUR context runs than that.
//
// The two must not be read separately. With a context shorter than the wait we
// abandon a call herdr is still completing: we report that nothing was sent,
// herdr goes on to deliver it anyway, the user sends again, and the
// conversation receives the same summary twice. The argv value below is
// derived from the same constant so the two cannot drift.
// Vars, not consts, for the same reason as timeout: a test has to shrink them
// to drive the paths they guard.
var (
	agentPromptWait  = 120 * time.Second
	agentPromptSlack = 15 * time.Second
)

func agentPromptArgv(agent, text string) []string {
	ms := strconv.FormatInt(agentPromptWait.Milliseconds(), 10)
	return []string{"agent", "prompt", agent, text, "--wait", "--timeout", ms}
}

// cmdWords identifies a herdr invocation by its subcommand only (e.g. "agent
// prompt"), never the full argv: some callers (AgentPrompt) pass message
// content as an argument, and that must never end up in an error string.
func cmdWords(args []string) string {
	n := len(args)
	if n > 2 {
		n = 2
	}
	return strings.Join(args[:n], " ")
}

// runError carries herdr's raw stderr bytes without embedding the argv that
// produced them, so a caller (AgentPrompt) can recover the JSON body to
// classify while every Error() string stays free of message content.
type runError struct {
	cmd    string
	stderr []byte
}

func (e *runError) Error() string {
	return fmt.Sprintf("herdr %s: %s", e.cmd, e.stderr)
}

func run(args ...string) ([]byte, error) {
	return runFor(timeout, args...)
}

// runFor is run with an explicit budget, for the one call whose own contract
// is longer than the default.
func runFor(budget time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	cmd := exec.CommandContext(ctx, Bin(), args...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("herdr %s timed out after %s", cmdWords(args), budget)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, &runError{cmd: cmdWords(args), stderr: ee.Stderr}
		}
		return nil, err
	}
	return out, nil
}

type paneCurrentResp struct {
	Result struct {
		Pane struct {
			PaneID       string `json:"pane_id"`
			CWD          string `json:"cwd"`
			Agent        string `json:"agent"`
			AgentSession *struct {
				Value string `json:"value"`
			} `json:"agent_session"`
		} `json:"pane"`
	} `json:"result"`
}

func parsePaneCurrent(b []byte) (adapter.Pane, error) {
	var r paneCurrentResp
	if err := json.Unmarshal(b, &r); err != nil {
		return adapter.Pane{}, err
	}
	p := adapter.Pane{
		ID:    r.Result.Pane.PaneID,
		CWD:   r.Result.Pane.CWD,
		Agent: r.Result.Pane.Agent,
	}
	if r.Result.Pane.AgentSession != nil {
		p.AgentSessionID = r.Result.Pane.AgentSession.Value
	}
	if p.ID == "" {
		return adapter.Pane{}, errors.New("herdr returned no pane")
	}
	return p, nil
}

// PaneCurrent describes the pane this process was invoked from.
func PaneCurrent() (adapter.Pane, error) {
	out, err := run("pane", "current", "--current")
	if err != nil {
		return adapter.Pane{}, err
	}
	return parsePaneCurrent(out)
}

type splitResp struct {
	Result struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	} `json:"result"`
}

func parseSplit(b []byte) (string, error) {
	var r splitResp
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	if r.Result.Pane.PaneID == "" {
		return "", errors.New("herdr returned no pane id")
	}
	return r.Result.Pane.PaneID, nil
}

// Split opens a sibling pane to the right, taking focus only when asked. A
// branch opens beside the conversation the user is in and must not pull them
// away; a replacement is where they are going next.
func Split(cwd string, focus bool) (string, error) {
	if err := checkArg("cwd", cwd); err != nil {
		return "", err
	}
	out, err := run(splitArgv(cwd, focus)...)
	if err != nil {
		return "", err
	}
	return parseSplit(out)
}

// AgentStart launches Claude in an existing pane, resuming a session.
func AgentStart(name, paneID, sessionID string) error {
	for _, c := range []struct{ what, v string }{
		{"agent name", name}, {"pane id", paneID}, {"session id", sessionID},
	} {
		if err := checkArg(c.what, c.v); err != nil {
			return err
		}
	}
	_, err := run(agentStartArgv(name, paneID, sessionID)...)
	return err
}

// OpenTreePane asks Herdr to open this plugin's overlay pane, telling it
// which session the calling pane is running.
func OpenTreePane(cwd, sessionID string) error {
	if err := checkArg("cwd", cwd); err != nil {
		return err
	}
	_, err := run(openTreePaneArgv(cwd, sessionID)...)
	return err
}

// classifyAgentError turns herdr's JSON error into a sentinel where we have
// one. A body with no error object is not an error.
func classifyAgentError(b []byte) error {
	var r struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &r) != nil || r.Error == nil {
		return nil
	}
	if r.Error.Code == "agent_blocked" {
		return ErrAgentBlocked
	}
	return fmt.Errorf("herdr: %s: %s", r.Error.Code, r.Error.Message)
}

// ErrNoLiveAgent means no agent herdr knows about is holding that session, so
// there is no conversation to continue. The caller grafts instead.
var ErrNoLiveAgent = errors.New("no live agent holds that session")

type agentListResp struct {
	Result struct {
		Agents []struct {
			PaneID       string `json:"pane_id"`
			AgentStatus  string `json:"agent_status"`
			AgentSession *struct {
				Value string `json:"value"`
			} `json:"agent_session"`
		} `json:"agents"`
	} `json:"result"`
}

type agentInfo struct{ pane, status string }

// parseAgents is parseAgentList with each agent's status kept, and the
// sessions two panes claim reported rather than silently dropped.
func parseAgents(b []byte) (map[string]agentInfo, map[string]bool, error) {
	var r agentListResp
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, nil, err
	}
	out := map[string]agentInfo{}
	ambiguous := map[string]bool{}
	for _, a := range r.Result.Agents {
		if a.PaneID == "" || a.AgentSession == nil || a.AgentSession.Value == "" {
			continue // an agent still starting has no session yet
		}
		sid := a.AgentSession.Value
		if _, seen := out[sid]; seen {
			ambiguous[sid] = true
		}
		out[sid] = agentInfo{pane: a.PaneID, status: a.AgentStatus}
	}
	for sid := range ambiguous {
		delete(out, sid)
	}
	return out, ambiguous, nil
}

// parseAgentList maps each live agent's session id to the PANE holding it.
//
// The pane, not the agent's name: herdr accepts either wherever it wants an
// agent, only some agents carry a name at all (the ones herdr-tree started
// do; a claude the user launched need not), and a pane id is unique by
// construction while a name is only unique among live agents.
//
// A session claimed by two panes is dropped rather than resolved to one of
// them. `agent_session.value` is the last session id OBSERVED in a pane, and
// v1 recorded a headless `-p --resume` silently overwriting it, so two panes
// reporting the same session is reachable — and guessing there would deliver
// a summary into a conversation the user was not looking at.
func parseAgentList(b []byte) (map[string]string, error) {
	agents, _, err := parseAgents(b)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(agents))
	for sid, a := range agents {
		out[sid] = a.pane
	}
	return out, nil
}

// AgentForSession returns the herdr target for the live agent holding
// sessionID — the id of its pane — or ErrNoLiveAgent.
func AgentForSession(sessionID string) (string, error) {
	if err := checkArg("session id", sessionID); err != nil {
		return "", err
	}
	out, err := run(agentListArgv()...)
	if err != nil {
		return "", err
	}
	bySession, err := parseAgentList(out)
	if err != nil {
		return "", err
	}
	target, ok := bySession[sessionID]
	if !ok {
		return "", fmt.Errorf("%s: %w", sessionID, ErrNoLiveAgent)
	}
	return target, nil
}

// AgentState reports the pane holding sessionID and herdr's agent_status for
// it ("working", "idle", …). No agent holding it is not an error: both are
// empty. Two panes claiming it is, because the caller is about to close one.
func AgentState(sessionID string) (pane, status string, err error) {
	if err := checkArg("session id", sessionID); err != nil {
		return "", "", err
	}
	out, err := run(agentListArgv()...)
	if err != nil {
		return "", "", err
	}
	agents, ambiguous, err := parseAgents(out)
	if err != nil {
		return "", "", err
	}
	if ambiguous[sessionID] {
		return "", "", fmt.Errorf("%s is open in more than one pane", sessionID)
	}
	a := agents[sessionID]
	return a.pane, a.status, nil
}

// ClosePane closes a pane. herdr does not refuse a busy one; the caller
// checks agent_status first.
func ClosePane(paneID string) error {
	if err := checkArg("pane id", paneID); err != nil {
		return err
	}
	_, err := run("pane", "close", paneID)
	return err
}

// AgentPrompt sends text to a running agent as if typed. A successful return
// means herdr accepted and delivered the message; it does not mean the agent
// has started, let alone finished, a turn on it.
//
// text is deliberately NOT passed through checkArg: a summary legitimately
// begins with "⤶", and it is a positional argument after the agent name, not
// a flag position.
func AgentPrompt(agent, text string) error {
	if err := checkArg("agent name", agent); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("refusing to send an empty message")
	}
	// The message sits before --wait in the argv, so a leading dash is read
	// as a flag: herdr would reject it AND quote it back in its own stderr,
	// putting conversation content in an error string. checkArg cannot be
	// used here — it echoes the value it rejects.
	if strings.HasPrefix(text, "-") {
		return fmt.Errorf("message begins with a dash: %w", ErrUnsafeArgument)
	}
	out, err := runFor(agentPromptWait+agentPromptSlack, agentPromptArgv(agent, text)...)
	if err != nil {
		// herdr reports its errors (including agent_blocked) as JSON on
		// stderr with a non-zero exit, which cmd.Output() turns into an
		// error before we ever see stdout. Recover that stderr body so a
		// blocked agent is still classifiable.
		var re *runError
		if errors.As(err, &re) {
			if ce := classifyAgentError(re.stderr); ce != nil {
				return ce
			}
		}
		return err
	}
	return classifyAgentError(out)
}
