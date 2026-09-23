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
	SessionPath   string
	SessionTitle  string
	IsSessionRoot bool
	IsSessionLeaf bool // the last turn of this session's own chain
	Grafted       bool // this node starts a session branched from its parent
	Broken        bool // session present but unreadable or empty
	FromRemoved   bool // a branch whose turn was removed from the line it left
	CutHere       int  // turns cut immediately before this entry
	CutAfter      int  // turns cut after this entry, which ends its line
	Label         string
	// IsHead marks a section head: a human prompt, or the first entry of a
	// session that does not start with one. Everything until the next head is
	// that section's body.
	IsHead   bool
	Children []*Node
}

// Build turns sessions plus graft edges into roots. A session is a chain;
// a graft edge nests one chain under a turn of another.
func Build(sessions []adapter.Session, s *store.Store) []*Node {
	chains := make(map[string]*Node, len(sessions)) // session id -> its first node
	nodeIndex := make(map[string]map[string]*Node)  // session id -> turn id -> node

	// A replaced session is hidden, but only when what replaced it is here
	// to take its place: a missing replacement must not take the
	// conversation out of view with it.
	present := make(map[string]bool, len(sessions))
	for _, sess := range sessions {
		present[sess.ID] = true
	}
	hidden := map[string]bool{}
	for id, br := range s.Branches {
		if br.ReplacedBy != "" && present[s.Resolve(id)] {
			hidden[id] = true
		}
	}
	leafOf := map[string]*Node{}

	for _, sess := range sessions {
		if hidden[sess.ID] {
			continue
		}
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
				Node: t, SessionID: sess.ID, SessionCWD: sess.CWD,
				SessionPath: sess.Path, SessionTitle: sess.Title,
				IsSessionRoot: i == 0,
				IsSessionLeaf: i == len(sess.Nodes)-1,
				Broken:        sess.Broken,
			}
			n.Label = s.Labels[store.LabelKey(sess.ID, t.ID)]
			nodeIndex[sess.ID][t.ID] = n

			isHead := t.Kind == adapter.KindHuman || head == nil
			switch {
			case head == nil && prev == nil:
				// first node of the session
			case isHead:
				// a new section hangs off the previous section head, so
				// sections form a chain the indent rule keeps level
				head.Children = append(head.Children, n)
			default:
				// body of the current section
				head.Children = append(head.Children, n)
			}
			n.IsHead = isHead
			if isHead {
				head = n
			}
			if n.IsSessionLeaf {
				leafOf[sess.ID] = n
			}
			if prev == nil {
				chains[sess.ID] = n
			}
			prev = n
		}
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
		from := br.GraftedFrom.SessionID
		resolved := from
		if hidden[from] {
			resolved = s.Resolve(from)
		}
		parentNodes, ok := nodeIndex[resolved]
		if !ok {
			continue // parent session gone: child stays a root
		}
		parent, ok := parentNodes[br.GraftedFrom.Node]
		if !ok {
			// The turn it left was spliced out of the line that replaced
			// its parent. It stays a root, and says why.
			child.FromRemoved = resolved != from
			continue
		}
		child.Grafted = true
		parent.Children = append(parent.Children, child)
		attached[childSID] = true
	}

	for id, br := range s.Branches {
		if br.Cut == nil || !present[id] || hidden[id] {
			continue
		}
		if n := nodeIndex[id][br.Cut.At]; n != nil {
			n.CutHere = br.Cut.Turns
		} else if n := leafOf[id]; n != nil {
			n.CutAfter = br.Cut.Turns
		}
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
