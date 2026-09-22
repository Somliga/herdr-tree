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
//
// The adapter check belongs HERE, not in pane(). This runs as a Herdr action
// in the pane the user is looking at, so it can see which agent that pane is
// running. pane() runs inside the overlay Herdr then opens, and an overlay
// pane carries no agent at all — the check there could never fire.
func open() error {
	p, err := herdr.PaneCurrent()
	if err != nil {
		return err
	}
	if p.Agent != "" && p.Agent != "claude" {
		return fmt.Errorf("no adapter for %s", p.Agent)
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
	// Where to send a summary that lands at the live session's tip. herdr
	// accepts a pane id wherever it accepts an agent, and `agent list`
	// carries each live agent's session id, so the session resolves to a
	// target without needing a name — which not every agent has.
	//
	// A session nobody is holding, or a herdr that will not answer, leaves
	// this empty and the fold-back grafts instead. That is the right
	// fallback: there is no conversation to continue. The overlay says which
	// of the two it is about to do before the key that commits to it, so the
	// difference is never silent, and there is nowhere to report an error to
	// from here — the overlay owns the screen from the next line on.
	liveAgent := ""
	if current != "" {
		if target, err := herdr.AgentForSession(current); err == nil {
			liveAgent = target
		}
	}
	return tui.Run(a, root, st, sessions, current, liveAgent, herdr.AgentPrompt)
}
