package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
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

// headCWD reads only far enough to find the first cwd, so Discover can reject
// a transcript belonging to another repository without decoding all of it.
// Most of the cost of opening the tree was parsing megabytes of sessions that
// were then discarded.
func headCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 0; n < 200 && sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var probe struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(line, &probe) == nil && probe.CWD != "" {
			return probe.CWD
		}
	}
	// No sc.Err() check: a scan error here just means no cwd was found in the
	// head, which already returns "" below and causes Discover to skip the
	// file — the correct outcome either way, not an oversight.
	return ""
}

// Discover returns every session belonging to repoRoot, newest first.
func Discover(repoRoot string) ([]adapter.Session, error) {
	pattern := filepath.Join(ProjectsDir(), "*", "*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	// repo.Root shells out to git; memoise per raw cwd so a repo with many
	// sessions from the same directory pays for it once.
	roots := map[string]string{}
	rootOf := func(cwd string) string {
		if r, ok := roots[cwd]; ok {
			return r
		}
		r, _, err := repo.Root(cwd)
		if err != nil {
			r = ""
		}
		roots[cwd] = r
		return r
	}

	var out []adapter.Session
	for _, p := range paths {
		head := headCWD(p)
		if head == "" || rootOf(head) != repoRoot {
			continue // belongs elsewhere (or unreadable): skip without a full parse
		}
		es, skipped, err := ParseFile(p)
		if err != nil || len(es) == 0 {
			continue // unreadable: cannot be attributed to any repo
		}
		cwd := sessionCWD(es)
		if cwd == "" {
			continue
		}
		root := rootOf(cwd)
		if root != repoRoot {
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
