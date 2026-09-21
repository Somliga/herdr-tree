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
	fmt.Println(sid)
	fmt.Fprintln(os.Stderr, "wrote", path)
}
