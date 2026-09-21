// Package tree assembles sessions and graft edges into the forest the TUI
// renders. It knows nothing about any agent's storage format.
package tree

import (
	"sort"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

type Node struct {
	Node       adapter.Node
	SessionID  string
	SessionCWD string // the session's own cwd, which may be a worktree
	// SessionPath is where the transcript was FOUND. It is carried all the
	// way to the TUI because a Session rebuilt from a tree node with only an
	// id and a cwd would fall back to reconstructing the path, which is wrong
	// for any session that relocated into a worktree — the exact bug this
	// field exists to prevent.
	SessionPath  string
	SessionTitle string
	IsSessionRoot bool
	IsSessionLeaf bool // the last turn of this session's own chain
	Grafted       bool // this node starts a session branched from its parent
	Broken        bool // session present but unreadable or empty
	Label         string
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
				SessionID: sess.ID, SessionCWD: sess.CWD, SessionPath: sess.Path, SessionTitle: sess.Title,
				IsSessionRoot: true, IsSessionLeaf: true, Broken: true,
			}
			chains[sess.ID] = n
			continue
		}
		var head, prev *Node
		for i, t := range sess.Nodes {
			n := &Node{
				Node: t, SessionID: sess.ID, SessionCWD: sess.CWD, SessionPath: sess.Path,
				SessionTitle: sess.Title,
				IsSessionRoot: i == 0, IsSessionLeaf: i == len(sess.Nodes)-1, Broken: sess.Broken,
			}
			n.Label = s.Labels[store.LabelKey(sess.ID, t.ID)]
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

	// Graft edges come out of a map, whose iteration order Go randomises per
	// run. Attaching in that order would reshuffle grafted siblings under a
	// turn between launches, and this tree is navigated by position. Order
	// them the way roots are ordered — by the session list, which arrives
	// newest-first — so the layout is stable and consistent.
	position := make(map[string]int, len(sessions))
	for i, sess := range sessions {
		position[sess.ID] = i
	}
	edges := make([]string, 0, len(s.Branches))
	for childSID := range s.Branches {
		edges = append(edges, childSID)
	}
	sort.Slice(edges, func(i, j int) bool {
		pi, oki := position[edges[i]]
		pj, okj := position[edges[j]]
		if oki != okj {
			return oki // sessions we know about come first
		}
		if pi != pj {
			return pi < pj
		}
		return edges[i] < edges[j]
	})

	attached := map[string]bool{}
	for _, childSID := range edges {
		br := s.Branches[childSID]
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
