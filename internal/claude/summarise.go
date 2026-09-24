package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// summariseTimeout bounds the model call. Summarising a long range is slow,
// and the overlay is waiting.
var summariseTimeout = 5 * time.Minute // a var so a test can shorten it

// summariseGraftHook, when set, sees the throwaway transcript before it is
// resumed. Tests only: it is how a test observes what the model is shown.
var summariseGraftHook func(path string)

// SummarisePrompt asks for the four things that make a summary worth folding
// back. "rejected, and why" matters most: it is what stops the trunk paying
// again for a dead end this branch already explored.
func SummarisePrompt(fromTitle, toTitle string) string {
	return fmt.Sprintf(`Summarise only the part of this conversation from the turn beginning %q up to and including the turn beginning %q. Ignore everything before that range except as context.

Write four short sections:
- attempted: what this stretch was trying to do.
- decided: what was settled, and the reasoning.
- rejected: what was tried and abandoned, and why not. Be specific; this is what stops the work being repeated.
- unfinished: what remains.

Be concise and concrete. Take no actions; reply with the summary text only.`,
		fromTitle, toTitle)
}

// CompactPrompt is for a compaction: the text replaces the range in its own
// line, so it is a handover to the same conversation — current state and
// exact identifiers first, what comes next last. SummarisePrompt is for a
// summary carried to another line, where what was rejected matters most.
func CompactPrompt(fromTitle, toTitle string) string {
	return fmt.Sprintf(`Compact the part of this conversation from the turn beginning %q up to and including the turn beginning %q. Your text will replace those turns: the conversation continues from it as if they had happened, so write what the continuation needs.

Write short sections:
- state: where the work stands at the end of the range — files, functions, values and settings that now exist or changed, named exactly.
- decisions: what was settled and why, including any constraint the user stated.
- dead ends: what was tried and failed, one line each, so it is not retried.
- next: what was about to happen next.

Be concise and concrete. Keep exact names, paths, commands and numbers verbatim. Take no actions; reply with the summary text only.`,
		fromTitle, toTitle)
}

// Summarise produces a summary of the turns between fromTurn and toTurn.
//
// It grafts the source at toTurn into a throwaway session, resumes that with
// the prompt, captures stdout and removes the throwaway. The graft is needed
// because --resume always continues at a session's tip: summarising "up to
// turn 12" must not let the model see turn 13.
func Summarise(srcPath, fromTurn, toTurn, tmpCWD string, compact bool) (string, error) {
	es, skipped, err := ParseFile(srcPath)
	if err != nil {
		return "", err
	}
	if skipped > 0 {
		return "", ErrPartialTranscript
	}
	var titles []string
	title := func(id string) string {
		for _, e := range es {
			if e.UUID() == id {
				return Title(e.Text(), 60)
			}
		}
		return id
	}

	// Whole turns, as a splice removes them: the model reads through the end
	// turn's last entry, and the prompt names each end by the turn's opening
	// entry — the thing the user typed.
	l, err := buildLine(es)
	if err != nil {
		return "", err
	}
	a, b, err := l.span(fromTurn, toTurn)
	if err != nil {
		return "", err
	}
	fromTurn, toTurn = l.firstOf(a), l.firstOf(b)

	sid, path, err := Graft(srcPath, l.lastOf(b), tmpCWD)
	if err != nil {
		return "", err
	}
	if summariseGraftHook != nil {
		summariseGraftHook(path)
	}
	// The one place this project removes a session: this exact path was
	// created moments ago, for this call alone, and nothing else can have
	// learned of it. Registered only once Graft has succeeded, so a failure
	// above never reaches this line with a zero-value path.
	//
	// The project DIRECTORY goes too. tmpCWD is fresh per call, so its slug is
	// fresh per call, and leaving it behind would deposit one more empty
	// directory in the user's ~/.claude/projects every time they summarise —
	// unbounded, and in the place their own sessions live. os.Remove refuses a
	// non-empty directory, so this can never take anything with it. Claude
	// Code also creates an empty memory/ inside a project; v1's graft
	// verification found that one the hard way.
	//
	// ponytail: a hardcoded list of one known empty child. If Claude Code ever
	// adds a second one to a fresh project, os.Remove(dir) starts failing
	// again and the accumulation above returns with nothing reporting it. The
	// test below pins today's shape only. Generalising this to "delete every
	// empty child" would trade a leak for the thing os.Remove is here to
	// prevent, so the ceiling is deliberate.
	defer func() {
		os.Remove(path)
		dir := filepath.Dir(path)
		os.Remove(filepath.Join(dir, "memory"))
		os.Remove(dir)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), summariseTimeout)
	defer cancel()
	fromTitle, toTitle := title(fromTurn), title(toTurn)
	titles = append(titles, fromTitle, toTitle)
	prompt := SummarisePrompt(fromTitle, toTitle)
	if compact {
		prompt = CompactPrompt(fromTitle, toTitle)
	}
	cmd := exec.CommandContext(ctx, "claude", "-p", "--resume", sid, prompt)
	cmd.Dir = tmpCWD
	cmd.Stdin = nil
	// The kill reaches only claude itself. Anything it started still holds
	// stdout open, and Output would wait on that past the timeout.
	// ponytail: the orphan is left running; kill its process group if one is
	// ever seen outliving a timeout.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("summarise timed out after %s", summariseTimeout)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // claude exited cleanly and its reply is whole; only a child lingered
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// The prompt we sent carries two turn titles, which are message
			// content. If the CLI ever quotes its prompt argument back, that
			// stderr becomes a status line holding the user's own words. The
			// same route was already closed on the Herdr side; close it here
			// rather than rely on a CLI's choice of diagnostics.
			return "", fmt.Errorf("claude: %s", scrubTitles(string(ee.Stderr), titles))
		}
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("claude returned an empty summary")
	}
	return text, nil
}

// scrubTitles removes any turn title from a diagnostic before it becomes a
// status line. Titles are the user's own words; a CLI's stderr is not a place
// we control, so what we hand onward is filtered rather than trusted.
func scrubTitles(s string, titles []string) string {
	for _, t := range titles {
		if strings.TrimSpace(t) == "" {
			continue
		}
		s = strings.ReplaceAll(s, t, "…")
	}
	return strings.TrimSpace(s)
}
