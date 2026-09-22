package tree

// Trunk returns the set of session ids on the current lineage: the session you
// are in, and every session it was grafted from, back to a root.
//
// Nothing about the trunk is stored or marked. It is computed at render time
// from wherever you happen to be, so rewinding — which puts you in the new
// session — moves the trunk for free. An explicit pointer would need
// maintaining and could disagree with where you actually are.
//
// An empty current session yields an empty set, and the caller falls back to
// presenting every session as a root.
func Trunk(roots []*Node, currentSession string) map[string]bool {
	on := map[string]bool{}
	if currentSession == "" {
		return on
	}
	parentOf := map[string]string{}
	var walk func(n *Node)
	seen := map[*Node]bool{}
	walk = func(n *Node) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, c := range n.Children {
			if c.SessionID != n.SessionID {
				parentOf[c.SessionID] = n.SessionID
			}
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	for sid := currentSession; sid != ""; sid = parentOf[sid] {
		if on[sid] {
			break // a cycle in a hand-edited store must not hang this
		}
		on[sid] = true
	}
	return on
}
