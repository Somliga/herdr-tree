package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
