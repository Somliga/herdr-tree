// Package repo resolves a working directory to the repository that owns it,
// so that every git worktree of one repo shares a single tree.
package repo

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Root returns the directory that owns dir. For a git worktree this is the
// main repository, so all worktrees group together. For a directory that is
// not in a repository it returns the directory itself with isGit false,
// which is a supported mode, not an error.
func Root(dir string) (root string, isGit bool, err error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	cmd := exec.Command("git", "-C", resolved,
		"rev-parse", "--path-format=absolute", "--git-common-dir")
	out, gerr := cmd.Output()
	if gerr != nil {
		return resolved, false, nil
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return resolved, false, nil
	}
	r := filepath.Dir(common)
	if rr, e := filepath.EvalSymlinks(r); e == nil {
		r = rr
	}
	return r, true, nil
}
