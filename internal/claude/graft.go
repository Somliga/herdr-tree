package claude

import "errors"

// ErrNodeNotFound means the requested graft point is not in the transcript.
var ErrNodeNotFound = errors.New("graft node not found in transcript")

// Select returns the set of entry uuids a graft at atNode must keep.
//
// Rule 1: the ancestor chain of atNode.
// Rule 2: assistant entries sharing a kept entry's requestId, because one
//         assistant response is written as several entries, one per block.
// Rule 3: attachment entries whose parent is kept.
//
// The synthetic "Continue from where you left off." turn is kept: it is
// real conversation content, and only the tree view hides it.
func Select(es []Entry, atNode string) (map[string]bool, error) {
	byUUID := make(map[string]Entry, len(es))
	for _, e := range es {
		if u := e.UUID(); u != "" {
			byUUID[u] = e
		}
	}
	if _, ok := byUUID[atNode]; !ok {
		return nil, ErrNodeNotFound
	}

	keep := map[string]bool{}

	// Rule 1.
	for cur := atNode; cur != ""; {
		e, ok := byUUID[cur]
		if !ok || keep[cur] {
			break // missing parent or a cycle: stop, do not loop forever
		}
		keep[cur] = true
		cur = e.ParentUUID()
	}

	// Rule 2.
	reqs := map[string]bool{}
	for u := range keep {
		if e := byUUID[u]; e.Type() == "assistant" && e.RequestID() != "" {
			reqs[e.RequestID()] = true
		}
	}
	for _, e := range es {
		if e.Type() == "assistant" && e.RequestID() != "" && reqs[e.RequestID()] {
			if u := e.UUID(); u != "" {
				keep[u] = true
			}
		}
	}

	// Rule 3.
	for _, e := range es {
		if e.Type() == "attachment" && keep[e.ParentUUID()] {
			if u := e.UUID(); u != "" {
				keep[u] = true
			}
		}
	}

	return keep, nil
}
