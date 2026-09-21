// Command graftcheck grafts a transcript and prints the new session id.
// It exists for scripts/verify-graft.sh and is not part of the plugin.
package main

import (
	"fmt"
	"os"

	"herdr-tree/internal/claude"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: graftcheck <src.jsonl> <node-uuid> <dst-cwd>")
		os.Exit(2)
	}
	sid, path, err := claude.Graft(os.Args[1], os.Args[2], os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "graft:", err)
		os.Exit(1)
	}
	// Line 1 is the session id, line 2 is the file written. The script reads
	// both so it can clean up the session afterwards.
	fmt.Println(sid)
	fmt.Println(path)
}
