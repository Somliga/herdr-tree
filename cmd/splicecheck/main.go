// Command splicecheck splices every local transcript at many points into a
// temp directory and checks that each result is a transcript Claude Code
// could have written: every tool_use answered, every parent present, one
// root. Read-only on the user's files; prints counts, never content.
package main

import (
	"errors"
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
	// ponytail: defer doesn't cover interrupted processes; leftover -splicecheck dirs must be rmdir'd
	defer os.RemoveAll(tmp)
	os.Setenv("CLAUDE_PROJECTS_DIR", tmp) // Splice writes here, never into src

	var tried, ok, inherited, bad int
	var refusedNotOnLine, refusedNothingLeft, refusedVersion, refusedPartial, refusedUnmarked, refusedOther int

	for _, p := range paths {
		es, skipped, err := claude.ParseFile(p)
		if err != nil || skipped > 0 {
			continue
		}
		orphanUses, orphanResults := getSourceOrphans(es)
		sourceOrphans := struct{ orphanUses, orphanResults map[string]bool }{orphanUses, orphanResults}

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
					r, refusReason := check(p, e, sourceOrphans)
					switch r {
					case "":
						ok++
					case "refused":
						recordRefusal(refusReason, &refusedNotOnLine, &refusedNothingLeft, &refusedVersion, &refusedPartial, &refusedUnmarked, &refusedOther)
					case "inherited":
						inherited++
					default:
						bad++
						fmt.Printf("BAD %s turn %d+%d seed=%v: %s\n", shortID(p), i+1, w, e.Seed != "", r)
					}
				}
			}
			tried++
			r, refusReason := check(p, adapter.Edit{After: prompts[i], Seed: claude.SummaryPrefix + " check"}, sourceOrphans)
			switch r {
			case "":
				ok++
			case "refused":
				recordRefusal(refusReason, &refusedNotOnLine, &refusedNothingLeft, &refusedVersion, &refusedPartial, &refusedUnmarked, &refusedOther)
			case "inherited":
				inherited++
			default:
				bad++
				fmt.Printf("BAD %s insert after turn %d: %s\n", shortID(p), i+1, r)
			}
		}
	}
	totalRefused := refusedNotOnLine + refusedNothingLeft + refusedVersion + refusedPartial + refusedUnmarked + refusedOther
	fmt.Printf("sessions %d · splices %d · valid %d · inherited %d · refused %d (not-on-line %d, nothing-left %d, version %d, partial %d, unmarked %d, other %d) · BAD %d\n",
		len(paths), tried, ok, inherited, totalRefused, refusedNotOnLine, refusedNothingLeft, refusedVersion, refusedPartial, refusedUnmarked, refusedOther, bad)
	if bad > 0 {
		os.Exit(1)
	}
}

func getSourceOrphans(es []claude.Entry) (orphanUses, orphanResults map[string]bool) {
	orphanUses = map[string]bool{}
	orphanResults = map[string]bool{}
	uses, results := map[string]bool{}, map[string]bool{}
	for _, en := range es {
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
	for id := range uses {
		if !results[id] {
			orphanUses[id] = true
		}
	}
	for id := range results {
		if !uses[id] {
			orphanResults[id] = true
		}
	}
	return
}

func shortID(p string) string {
	b := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

func recordRefusal(refusReason error, notOnLine, nothingLeft, version, partial, unmarked, other *int) {
	if refusReason == nil {
		*other++
		return
	}
	if errors.Is(refusReason, claude.ErrNotOnLine) {
		*notOnLine++
	} else if errors.Is(refusReason, claude.ErrNothingLeft) {
		*nothingLeft++
	} else if errors.Is(refusReason, claude.ErrUnsupportedVersion) {
		*version++
	} else if errors.Is(refusReason, claude.ErrPartialTranscript) {
		*partial++
	} else if errors.Is(refusReason, claude.ErrUnmarkedSeed) {
		*unmarked++
	} else {
		*other++
	}
}

// check splices once and validates the result. returns ("", nil) for valid,
// ("refused", err) for a splice Splice declined, ("inherited", nil) for
// defects inherited from the source, and (defect name, nil) for introduced defects.
func check(path string, e adapter.Edit, sourceOrphans struct{ orphanUses, orphanResults map[string]bool }) (string, error) {
	res, err := claude.Splice(path, e, "/splicecheck")
	if err != nil {
		return "refused", err
	}
	out, _ := filepath.Glob(filepath.Join(claude.ProjectsDir(), "*", res.SessionID+".jsonl"))
	if len(out) != 1 {
		return "no output file", nil
	}
	defer os.Remove(out[0])
	es, skipped, err := claude.ParseFile(out[0])
	if err != nil || skipped > 0 {
		return "output does not parse", nil
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
			return "an entry's parent is missing", nil
		}
	}
	if roots != 1 {
		return fmt.Sprintf("%d roots", roots), nil
	}
	for id := range uses {
		if !results[id] {
			if sourceOrphans.orphanUses[id] {
				return "inherited", nil
			}
			return "a tool_use has no tool_result", nil
		}
	}
	for id := range results {
		if !uses[id] {
			if sourceOrphans.orphanResults[id] {
				return "inherited", nil
			}
			return "a tool_result has no tool_use", nil
		}
	}
	return "", nil
}
