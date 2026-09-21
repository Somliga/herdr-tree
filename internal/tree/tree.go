// Package tree assembles sessions and graft edges into the forest the TUI
// renders. It knows nothing about any agent's storage format.
package tree

import (
	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

type Node struct {
	Node          adapter.Node
	SessionID     string
	SessionCWD    string // the session's own cwd, which may be a worktree
	SessionTitle  string
	IsSessionRoot bool
	Grafted       bool // this node starts a session branched from its parent
	Broken        bool // session present but unreadable or empty
	Children      []*Node
}

// Build turns sessions plus graft edges into roots. A session is a chain;
// a graft edge nests one chain under a turn of another.
func Build(sessions []adapter.Session, s *store.Store) []*Node {
	chains := make(map[string]*Node, len(sessions))   // session id -> its first node
	nodeIndex := make(map[string]map[string]*Node)    // session id -> turn id -> node

	for _, sess := range sessions {
		nodeIndex[sess.ID] = map[string]*Node{}
		if len(sess.Nodes) == 0 {
			n := &Node{
				SessionID: sess.ID, SessionCWD: sess.CWD, SessionTitle: sess.Title,
				IsSessionRoot: true, Broken: true,
			}
			chains[sess.ID] = n
			continue
		}
		var head, prev *Node
		for i, t := range sess.Nodes {
			n := &Node{
				Node: t, SessionID: sess.ID, SessionCWD: sess.CWD,
				SessionTitle: sess.Title,
				IsSessionRoot: i == 0, Broken: sess.Broken,
			}
			nodeIndex[sess.ID][t.ID] = n
			if prev == nil {
				head = n
			} else {
				prev.Children = append(prev.Children, n)
			}
			prev = n
		}
		chains[sess.ID] = head
	}

	attached := map[string]bool{}
	for childSID, br := range s.Branches {
		child, ok := chains[childSID]
		if !ok {
			continue // session gone; nothing to attach
		}
		parentNodes, ok := nodeIndex[br.GraftedFrom.SessionID]
		if !ok {
			continue // parent session gone: child stays a root
		}
		parent, ok := parentNodes[br.GraftedFrom.Node]
		if !ok {
			continue // parent turn gone: child stays a root
		}
		child.Grafted = true
		parent.Children = append(parent.Children, child)
		attached[childSID] = true
	}

	var roots []*Node
	for _, sess := range sessions {
		if attached[sess.ID] {
			continue
		}
		if n := chains[sess.ID]; n != nil {
			roots = append(roots, n)
		}
	}
	// sessions arrive newest first and roots are appended in that order
	return roots
}
