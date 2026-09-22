package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
)

func TestSummarisePromptNamesBothEnds(t *testing.T) {
	p := SummarisePrompt("i want to discuss the weather", "good conclusion")
	for _, want := range []string{
		"i want to discuss the weather",
		"good conclusion",
		"rejected",
		"unfinished",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt does not mention %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "tool") {
		t.Fatal("the prompt must not invite tool use; it is a -p call")
	}
}

// stubClaude puts a fake `claude` first on PATH. It exercises the real
// subprocess plumbing — argv, stdin, the timeout, and the removal of the
// throwaway session — without spending an API call. Everything about the
// REPLY still needs Task 10; everything around it does not.
func stubClaude(t *testing.T, script string) (argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done >> " + argvFile + "\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvFile
}

func sessionFiles(t *testing.T, projects string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(projects, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestSummariseRemovesTheThrowawaySession(t *testing.T) {
	for _, c := range []struct {
		name, script string
		wantErr      bool
	}{
		{"success", `echo "attempted: x"`, false},
		{"claude fails", `echo boom >&2; exit 1`, true},
		{"empty reply", `true`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			projects := t.TempDir()
			t.Setenv("CLAUDE_PROJECTS_DIR", projects)
			stubClaude(t, c.script)

			_, err := Summarise("testdata/simple.jsonl", "u1", "u3", t.TempDir())
			if c.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if left := sessionFiles(t, projects); len(left) != 0 {
				t.Fatalf("the throwaway session survived: %v", left)
			}
		})
	}
}

func TestSummarisePassesThePromptAsOneArgument(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	argvFile := stubClaude(t, `echo ok`)
	if _, err := Summarise("testdata/simple.jsonl", "u1", "u3", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	if len(args) != 4 || args[0] != "-p" || args[1] != "--resume" {
		t.Fatalf("argv = %q, want -p --resume <sid> <prompt> as four arguments", args)
	}
	// The prompt is one argv element however many newlines and quotes it
	// holds — never a shell string.
	if !strings.Contains(args[3], "Summarise only the part") {
		t.Fatalf("the prompt is not the fourth argument: %q", args[3])
	}
}

// An interrupted summarise never runs the deferred cleanup — ctrl+c reaches
// tea.Quit on purpose, because the call can take minutes. What matters then
// is WHERE the throwaway lands. Handed the session's own cwd, the orphan
// appears in the user's tree as a new root session, because Discover scans
// exactly that directory. So the adapter must hand Summarise somewhere else.
//
// Asserting the directory is empty afterwards would prove nothing: on a
// successful run the deferred removal tidies up either way. What this pins is
// the cwd the summarising process actually runs in.
func TestSummariseRunsOutsideTheSessionsOwnProjectDirectory(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	dir := t.TempDir()
	pwdFile := filepath.Join(dir, "pwd")
	stubClaude(t, "printf '%s' \"$PWD\" > "+pwdFile+"; echo ok")

	repo := t.TempDir()
	ownDir := filepath.Join(projects, SlugFor(repo))
	if err := os.MkdirAll(ownDir, 0700); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	sid := "11111111-1111-4111-8111-111111111111"
	if err := os.WriteFile(filepath.Join(ownDir, sid+".jsonl"), src, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := New().Summarise(adapter.Session{ID: sid, CWD: repo}, "u1", "u3"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(pwdFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == repo {
		t.Fatalf("summarised in the session's own cwd %q, so its throwaway lands where Discover scans", repo)
	}
}

// The prompt carries two turn titles — the user's own words. If the CLI ever
// quotes its prompt argument back on stderr, that error becomes a status line
// holding them. The identical route was closed on the Herdr side; a CLI's
// choice of diagnostics is not something to rely on.
func TestSummariseErrorDoesNotRepeatTheTurnTitles(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	// simple.jsonl's u1 is titled "first question"; echo the prompt back the
	// way a CLI complaining about its arguments would.
	stubClaude(t, `printf 'bad argument: %s\n' "$4" >&2; exit 1`)

	_, err := Summarise("testdata/simple.jsonl", "u1", "u3", t.TempDir())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, leaked := range []string{"first question", "second question"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("the error repeated a turn title %q: %v", leaked, err)
		}
	}
}
