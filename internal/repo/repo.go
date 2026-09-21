// Package repo resolves a working directory to the repository that owns it,
// so that every git worktree of one repo shares a single tree.
package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Root returns the directory that owns dir. For a git worktree this is the
// main repository, so all worktrees group together. For a directory that is
// not in a repository it returns the directory itself with isGit false,
// which is a supported mode, not an error.
//
// dir need not still exist. A removed git worktree is ordinary in this
// workflow, and the sessions recorded inside it are exactly the history a
// user wants to look back at. When dir is gone, Root walks up to the nearest
// surviving ancestor and resolves that instead, so those sessions stay
// attributed to their repository rather than silently vanishing from the
// tree. The walk cannot mis-attribute: it only ever yields a repository that
// genuinely contains the missing path.
func Root(dir string) (root string, isGit bool, err error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}

	probe := resolved
	for {
		if fi, e := os.Stat(probe); e == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe { // reached the filesystem root
			return resolved, false, nil
		}
		probe = parent
	}

	cmd := exec.Command("git", "-C", probe,
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
