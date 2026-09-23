// Command splicecheck splices every local transcript at many points into a
// temp directory and checks that each result is a transcript Claude Code
// could have written: every tool_use answered, every parent present, one
// root. Read-only on the user's files; prints counts, never content.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/claude"
)

func main() {
	src := claude.ProjectsDir()
	paths, _ := filepath.Glob(filepath.Join(src, "*", "*.jsonl"))
	tmp, err := os.MkdirTemp("", "splicecheck-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)
	os.Setenv("CLAUDE_PROJECTS_DIR", tmp) // Splice writes here, never into src

	var tried, ok, refused, bad int
	for _, p := range paths {
		es, skipped, err := claude.ParseFile(p)
		if err != nil || skipped > 0 {
			continue
		}
		var prompts []string
		for _, e := range es {
			if claude.IsPrompt(e) {
				prompts = append(prompts, e.UUID())
			}
		}
		// ponytail: windows of one to three turns, plus one insert per turn,
		// not every range. O(turns) splices per session keeps it to seconds;
		// widen the window if a failure ever hides in longer ranges.
		for i := range prompts {
			for w := 0; w < 3 && i+w < len(prompts); w++ {
				for _, e := range []adapter.Edit{
					{From: prompts[i], To: prompts[i+w]},
					{From: prompts[i], To: prompts[i+w], Seed: claude.CompactionPrefix + " check"},
				} {
					tried++
					switch r := check(p, e); r {
					case "":
						ok++
					case "refused":
						refused++
					default:
						bad++
						fmt.Printf("BAD %s turn %d+%d seed=%v: %s\n", shortID(p), i+1, w, e.Seed != "", r)
					}
				}
			}
			tried++
			switch r := check(p, adapter.Edit{After: prompts[i], Seed: claude.SummaryPrefix + " check"}); r {
			case "":
				ok++
			case "refused":
				refused++
			default:
				bad++
				fmt.Printf("BAD %s insert after turn %d: %s\n", shortID(p), i+1, r)
			}
		}
	}
	fmt.Printf("sessions %d · splices %d · valid %d · refused %d · BAD %d\n", len(paths), tried, ok, refused, bad)
	if bad > 0 {
		os.Exit(1)
	}
}

func shortID(p string) string {
	b := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// check splices once and validates the result. "" is valid; "refused" is a
// splice the code declined; anything else names the defect.
func check(path string, e adapter.Edit) string {
	res, err := claude.Splice(path, e, "/splicecheck")
	if err != nil {
		return "refused"
	}
	out, _ := filepath.Glob(filepath.Join(claude.ProjectsDir(), "*", res.SessionID+".jsonl"))
	if len(out) != 1 {
		return "no output file"
	}
	defer os.Remove(out[0])
	es, skipped, err := claude.ParseFile(out[0])
	if err != nil || skipped > 0 {
		return "output does not parse"
	}
	have := map[string]bool{}
	uses, results := map[string]bool{}, map[string]bool{}
	for _, en := range es {
		if u := en.UUID(); u != "" {
			have[u] = true
		}
		msg, _ := en.Raw["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			switch bm["type"] {
			case "tool_use":
				id, _ := bm["id"].(string)
				uses[id] = true
			case "tool_result":
				id, _ := bm["tool_use_id"].(string)
				results[id] = true
			}
		}
	}
	roots := 0
	for _, en := range es {
		if en.UUID() == "" {
			continue
		}
		p := en.ParentUUID()
		if p == "" {
			roots++
		} else if !have[p] {
			return "an entry's parent is missing"
		}
	}
	if roots != 1 {
		return fmt.Sprintf("%d roots", roots)
	}
	for id := range uses {
		if !results[id] {
			return "a tool_use has no tool_result"
		}
	}
	for id := range results {
		if !uses[id] {
			return "a tool_result has no tool_use"
		}
	}
	return ""
}
