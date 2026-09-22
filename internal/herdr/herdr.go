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
	"strings"
	"time"

	"herdr-tree/internal/adapter"
)

// timeout bounds every herdr invocation. `agent start` waits for the agent to
// become ready (herdr's own default is 30s), and these calls are made from the
// TUI, so an unbounded wait is a permanently stuck overlay with no way out.
const timeout = 45 * time.Second

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

func splitArgv(cwd string) []string {
	return []string{"pane", "split", "--current", "--direction", "right", "--cwd", cwd, "--no-focus"}
}

func agentStartArgv(name, paneID, sessionID string) []string {
	return []string{"agent", "start", name, "--kind", "claude", "--pane", paneID, "--", "--resume", sessionID}
}

func openTreePaneArgv(cwd string) []string {
	return []string{"plugin", "pane", "open",
		"--plugin", "herdr-tree", "--entrypoint", "tree",
		"--placement", "overlay", "--cwd", cwd}
}

func agentPromptArgv(agent, text string) []string {
	return []string{"agent", "prompt", agent, text, "--wait", "--timeout", "120000"}
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, Bin(), args...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("herdr %s timed out after %s", cmdWords(args), timeout)
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

// Split opens a sibling pane to the right without stealing focus.
func Split(cwd string) (string, error) {
	if err := checkArg("cwd", cwd); err != nil {
		return "", err
	}
	out, err := run(splitArgv(cwd)...)
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

// OpenTreePane asks Herdr to open this plugin's overlay pane.
func OpenTreePane(cwd string) error {
	if err := checkArg("cwd", cwd); err != nil {
		return err
	}
	_, err := run(openTreePaneArgv(cwd)...)
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
	out, err := run(agentPromptArgv(agent, text)...)
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
