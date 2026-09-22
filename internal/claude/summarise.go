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
const summariseTimeout = 5 * time.Minute

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

// Summarise produces a summary of the turns between fromTurn and toTurn.
//
// It grafts the source at toTurn into a throwaway session, resumes that with
// the prompt, captures stdout and removes the throwaway. The graft is needed
// because --resume always continues at a session's tip: summarising "up to
// turn 12" must not let the model see turn 13.
func Summarise(srcPath, fromTurn, toTurn, tmpCWD string) (string, error) {
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

	sid, path, err := Graft(srcPath, toTurn, tmpCWD)
	if err != nil {
		return "", err
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
	cmd := exec.CommandContext(ctx, "claude", "-p", "--resume", sid,
		SummarisePrompt(fromTitle, toTitle))
	cmd.Dir = tmpCWD
	cmd.Stdin = nil
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("summarise timed out after %s", summariseTimeout)
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
