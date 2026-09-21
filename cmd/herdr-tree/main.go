// Command herdr-tree is the plugin binary. "open" is the action Herdr
// invokes from a keybinding; "pane" is the overlay Herdr then opens.
package main

import (
	"fmt"
	"os"

	"herdr-tree/internal/claude"
	"herdr-tree/internal/herdr"
	"herdr-tree/internal/repo"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tui"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "open":
		err = open()
	case "pane":
		err = pane()
	default:
		fmt.Fprintln(os.Stderr, "usage: herdr-tree <open|pane>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdr-tree:", err)
		os.Exit(1)
	}
}

// open asks Herdr to open the overlay in the focused pane's repo.
func open() error {
	p, err := herdr.PaneCurrent()
	if err != nil {
		return err
	}
	root, _, err := repo.Root(p.CWD)
	if err != nil {
		return err
	}
	return herdr.OpenTreePane(root)
}

// pane runs inside the overlay Herdr opened.
func pane() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, isGit, err := repo.Root(cwd)
	if err != nil {
		return err
	}
	// One adapter today. Refuse rather than mislead when the pane holds
	// something else: that refusal is the §10 boundary doing its job.
	if p, perr := herdr.PaneCurrent(); perr == nil && p.Agent != "" && p.Agent != "claude" {
		return fmt.Errorf("no adapter for %s", p.Agent)
	}

	a := claude.New()
	sessions, err := a.Discover(root)
	if err != nil {
		return err
	}
	st, err := store.Load(root)
	if err != nil {
		return err
	}

	current := ""
	if p, err := herdr.PaneCurrent(); err == nil {
		if sid, err := a.Current(p); err == nil {
			current = sid
		}
	}
	if !isGit {
		fmt.Fprintf(os.Stderr, "herdr-tree: %s is not a git repository; showing only this directory\n", root)
	}
	return tui.Run(a, root, st, sessions, current)
}
