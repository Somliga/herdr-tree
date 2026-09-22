// Package store persists the only data herdr-tree owns: the edge recording
// that one session was branched from a turn of another. Everything else in
// the tree is derived from transcripts and never duplicated here.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type From struct {
	SessionID string `json:"session_id"`
	Node      string `json:"node"`
}

type Branch struct {
	GraftedFrom From      `json:"grafted_from"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	Artifacts   []string  `json:"artifacts"` // always [], populated in v1.1
}

// Summary is an LLM summary of a RANGE of turns. It belongs to a span, not a
// session: the same session can be summarised over different spans, and
// folding one back has to name which.
type Summary struct {
	Text      string    `json:"text"`
	SessionID string    `json:"session_id"`
	FromTurn  string    `json:"from_turn"`
	ToTurn    string    `json:"to_turn"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	Version  int               `json:"version"`
	RepoRoot string            `json:"repo_root"`
	Branches map[string]Branch `json:"branches"`
	// Labels marks turns the user wants to find again, keyed "<session>:<turn>".
	// Landmarking a turn is deliberately separate from branching from it: in
	// practice you notice a point matters before you know whether you will go
	// back to it, and a label costs nothing while a branch costs a session.
	Labels    map[string]string `json:"labels,omitempty"`
	Summaries map[string]Summary `json:"summaries,omitempty"`

	path          string
	deletedLabels map[string]bool // keys cleared via SetLabel since Load, so Save's merge does not resurrect them
}

// Dir is Herdr's per-plugin config directory.
func Dir() string {
	if d := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); d != "" {
		return d
	}
	out, err := exec.Command("herdr", "plugin", "config-dir", "herdr-tree").Output()
	if err == nil {
		// Only accept something that looks like a path. A zero exit with a
		// warning or a diagnostic on stdout must fall through to the default,
		// not become the config directory.
		if d := strings.TrimSpace(string(out)); filepath.IsAbs(d) {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "plugins", "config", "herdr-tree")
}

func pathFor(repoRoot string) string {
	sum := sha256.Sum256([]byte(repoRoot))
	return filepath.Join(Dir(), hex.EncodeToString(sum[:])[:12], "tree.json")
}

// Load reads the store for a repo. A missing store is an empty store. A
// corrupt store is renamed aside and reported as empty: only graft edges
// are lost, and transcripts and artifacts are untouched.
func Load(repoRoot string) (*Store, error) {
	p := pathFor(repoRoot)
	s := &Store{Version: 1, RepoRoot: repoRoot, Branches: map[string]Branch{}, path: p}

	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var loaded Store
	if err := json.Unmarshal(b, &loaded); err != nil {
		// Timestamped so a second corruption does not overwrite the first.
		// A failed rename is deliberately ignored: returning a working empty
		// store matters more than preserving the backup, and the unreadable
		// file is left in place for the user to inspect.
		_ = os.Rename(p, fmt.Sprintf("%s.corrupt.%d", p, time.Now().UnixNano()))
		return s, nil
	}
	if loaded.Branches == nil {
		loaded.Branches = map[string]Branch{}
	}
	loaded.path = p
	loaded.RepoRoot = repoRoot
	if loaded.Version == 0 {
		loaded.Version = 1
	}
	return &loaded, nil
}

// LabelKey identifies a turn for labelling.
func LabelKey(sessionID, turnID string) string { return sessionID + ":" + turnID }

// SetLabel records or clears a landmark on a turn. An empty text removes it,
// so the same key toggles.
func (s *Store) SetLabel(sessionID, turnID, text string) {
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	k := LabelKey(sessionID, turnID)
	if text == "" {
		delete(s.Labels, k)
		if s.deletedLabels == nil {
			s.deletedLabels = map[string]bool{}
		}
		s.deletedLabels[k] = true
		return
	}
	delete(s.deletedLabels, k)
	s.Labels[k] = text
}

// SummaryKey identifies a span.
func SummaryKey(sessionID, fromTurn, toTurn string) string {
	return sessionID + ":" + fromTurn + ".." + toTurn
}

// AddSummary records or replaces the summary for a span.
func (s *Store) AddSummary(sum Summary) {
	if s.Summaries == nil {
		s.Summaries = map[string]Summary{}
	}
	s.Summaries[SummaryKey(sum.SessionID, sum.FromTurn, sum.ToTurn)] = sum
}

// sortSummaries orders summaries oldest first, with a tie-break on the key:
// the source is a map, whose iteration order Go randomises, and CreatedAt
// ties are ordinary (a caller may leave it zero). Without the tie-break the
// fold-back picker reshuffles between opens, which makes a list feel broken.
func sortSummaries(out []Summary) {
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return SummaryKey(out[i].SessionID, out[i].FromTurn, out[i].ToTurn) <
			SummaryKey(out[j].SessionID, out[j].FromTurn, out[j].ToTurn)
	})
}

// AllSummaries returns every summary in the store, oldest first. The
// fold-back picker offers summaries from every
// session, not just the one the cursor is in: folding a branch back is the
// whole point, and the branch is by definition a different session.
func (s *Store) AllSummaries() []Summary {
	out := make([]Summary, 0, len(s.Summaries))
	for _, v := range s.Summaries {
		out = append(out, v)
	}
	sortSummaries(out)
	return out
}

// Add records a graft edge, keyed by the new session's id.
func (s *Store) Add(sessionID string, b Branch) {
	if b.Artifacts == nil {
		b.Artifacts = []string{}
	}
	s.Branches[sessionID] = b
}

// ErrNoPath means Save was called on a Store that did not come from Load, so
// it has no file to write to. Without this guard filepath.Dir("") is ".", and
// Save would silently create tree.json in the process's working directory.
var ErrNoPath = errors.New("store has no path; use Load to obtain one")

// Save merges this store's branches into whatever is on disk now, then writes
// atomically.
//
// The merge matters: two Herdr panes can each Load, each Add a DIFFERENT
// branch, and each Save. A plain overwrite would silently discard the branch
// the other pane just created — and a graft edge is the one piece of data
// that exists nowhere else, so losing it orphans a real session in the tree.
// Re-reading first costs one file read and removes the whole race. v1 never
// deletes a branch, so a merge can never resurrect something intentionally
// removed.
func (s *Store) Save() error {
	if s.path == "" {
		return ErrNoPath
	}
	if onDisk, err := Load(s.RepoRoot); err == nil {
		for id, b := range onDisk.Branches {
			if _, ours := s.Branches[id]; !ours {
				s.Branches[id] = b
			}
		}
		for k, v := range onDisk.Labels {
			if s.deletedLabels[k] {
				continue // cleared locally since Load: do not resurrect it
			}
			if s.Labels == nil {
				s.Labels = map[string]string{}
			}
			if _, ours := s.Labels[k]; !ours {
				s.Labels[k] = v
			}
		}
		for k, v := range onDisk.Summaries {
			if s.Summaries == nil {
				s.Summaries = map[string]Summary{}
			}
			if _, ours := s.Summaries[k]; !ours {
				s.Summaries[k] = v
			}
		}
	}
	for id, b := range s.Branches {
		if b.Artifacts == nil {
			b.Artifacts = []string{}
			s.Branches[id] = b
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tree-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
