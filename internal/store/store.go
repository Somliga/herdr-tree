// Package store persists the only data herdr-tree owns: the edge recording
// that one session was branched from a turn of another. Everything else in
// the tree is derived from transcripts and never duplicated here.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

type Store struct {
	Version  int               `json:"version"`
	RepoRoot string            `json:"repo_root"`
	Branches map[string]Branch `json:"branches"`

	path string
}

// Dir is Herdr's per-plugin config directory.
func Dir() string {
	if d := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); d != "" {
		return d
	}
	out, err := exec.Command("herdr", "plugin", "config-dir", "herdr-tree").Output()
	if err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
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
		_ = os.Rename(p, p+".corrupt")
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

// Add records a graft edge, keyed by the new session's id.
func (s *Store) Add(sessionID string, b Branch) {
	if b.Artifacts == nil {
		b.Artifacts = []string{}
	}
	s.Branches[sessionID] = b
}

// Save writes atomically. Last writer wins if two panes race; only whole
// graft edges are at stake and they are independent.
func (s *Store) Save() error {
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
