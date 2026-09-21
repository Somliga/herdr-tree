package claude

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/repo"
)

// ProjectsDir is where Claude Code keeps transcripts. The environment
// variable exists so tests can point somewhere else.
func ProjectsDir() string {
	if d := os.Getenv("CLAUDE_PROJECTS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// SlugFor is Claude Code's directory name for a working directory.
func SlugFor(cwd string) string { return strings.ReplaceAll(cwd, "/", "-") }

// sessionCWD returns the first cwd recorded in a transcript. The directory
// name is a lossy encoding of the path, so it is never used for this.
func sessionCWD(es []Entry) string {
	for _, e := range es {
		if c := e.CWD(); c != "" {
			return c
		}
	}
	return ""
}

// Discover returns every session belonging to repoRoot, newest first.
func Discover(repoRoot string) ([]adapter.Session, error) {
	pattern := filepath.Join(ProjectsDir(), "*", "*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	var out []adapter.Session
	for _, p := range paths {
		es, skipped, err := ParseFile(p)
		if err != nil || len(es) == 0 {
			continue // unreadable: cannot be attributed to any repo
		}
		cwd := sessionCWD(es)
		if cwd == "" {
			continue
		}
		root, _, err := repo.Root(cwd)
		if err != nil || root != repoRoot {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		st, serr := os.Stat(p)
		var updated = es[len(es)-1].Timestamp()
		if serr == nil {
			updated = st.ModTime()
		}
		out = append(out, adapter.Session{
			ID:      id,
			CWD:     cwd,
			Title:   SessionTitle(es),
			Updated: updated,
			Nodes:   Turns(es),
			// A skipped line means the chain may have holes. Surface it as ⚠
			// rather than rendering a partial conversation as if complete.
			Broken: skipped > 0,
		})
	}
	// Stable so sessions with identical mtimes keep a deterministic order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}
