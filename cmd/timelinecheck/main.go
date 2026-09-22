// Command timelinecheck exercises claude.Summarise and claude.GraftSeeded
// for scripts/verify-timeline.sh. It is not part of the plugin.
package main

import (
	"fmt"
	"os"

	"herdr-tree/internal/claude"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "summarise":
		summarise(os.Args[2:])
	case "seed":
		seed(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: timelinecheck summarise <src.jsonl> <from-uuid> <to-uuid> <dst-cwd>")
	fmt.Fprintln(os.Stderr, "       timelinecheck seed <src.jsonl> <node-uuid> <dst-cwd> <summary-text>")
	os.Exit(2)
}

func summarise(args []string) {
	if len(args) != 4 {
		usage()
	}
	src, from, to, dstCWD := args[0], args[1], args[2], args[3]
	text, err := claude.Summarise(src, from, to, dstCWD)
	if err != nil {
		fmt.Fprintln(os.Stderr, "summarise:", err)
		os.Exit(1)
	}
	fmt.Println(text)
}

func seed(args []string) {
	if len(args) != 4 {
		usage()
	}
	src, node, dstCWD, text := args[0], args[1], args[2], args[3]
	// SummaryPrefix marks the entry as an injected summary, same as the TUI's
	// fold-back path (internal/tui/view.go). GraftSeeded rejects a seed that
	// doesn't start with it.
	s := claude.SummaryPrefix + " verify-timeline\n\n" + text
	sid, path, err := claude.GraftSeeded(src, node, dstCWD, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	// Line 1 is the session id, line 2 is the file written. The script reads
	// both so it can clean up afterwards.
	fmt.Println(sid)
	fmt.Println(path)
}
