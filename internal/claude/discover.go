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

// SlugFor is Claude Code's directory name for a working directory: every
// rune that is not a letter or digit becomes "-".
//
// Verified empirically against Claude Code 2.1.278 by running it in a
// directory named `slug_test.dir v2+x`, which produced `slug-test-dir-v2-x`:
// "/", "_", ".", " " and "+" all collapse to "-". It is per RUNE, not per
// byte — a real transcript here shows `Solör Bioenergi` becoming
// `Sol-r-Bioenergi`, one dash for a two-byte character.
//
// Getting this wrong is not cosmetic: a graft written into the wrong
// directory is a session Claude Code will never find, so the branch silently
// cannot be resumed.
func SlugFor(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

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
			Path:    p,
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
