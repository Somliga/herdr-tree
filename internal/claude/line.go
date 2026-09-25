package claude

import (
	"errors"

	"herdr-tree/internal/adapter"
)

// ErrNotOnLine means a selected entry is not on the line that ends at the
// session's tip: it sits on a stretch the session already rewound away from,
// so editing it would change nothing the agent reads.
var ErrNotOnLine = errors.New("that entry is not on this session's current line")

// line is a transcript's current line — what the agent reads on resume —
// divided into turns. A turn runs from something the user typed (or an entry
// we injected) up to, not including, the next one. Turn 0 is the preamble:
// whatever precedes the first prompt.
//
// Whole turns are the only safe unit to remove. Measured over 112 local
// transcripts, all 9146 tool_use/tool_result pairs lie inside one turn, and
// the only uuid reference crossing a boundary is each prompt's parentUuid.
type line struct {
	es    []Entry
	keep  map[string]bool // Select at the tip
	chain []string        // the parentUuid chain to the tip, root first
	turn  map[string]int  // every kept entry's turn
	last  int             // the highest turn number
}

// tipOf is where the session resumes: the last user or assistant entry of the
// main conversation, in file order. Sidechains are a subagent's own
// conversation; system and attachment entries are written after the reply
// they follow and parented to it, so taking one as the tip is harmless for
// system entries but an attachment parented to an earlier prompt would drop
// the reply entirely.
func tipOf(es []Entry) string {
	for i := len(es) - 1; i >= 0; i-- {
		e := es[i]
		if e.UUID() == "" || e.IsSidechain() {
			continue
		}
		if t := e.Type(); t == "user" || t == "assistant" {
			return e.UUID()
		}
	}
	return ""
}

// opensTurn reports whether e starts a turn.
func opensTurn(e Entry, hasOrigin bool) bool {
	k, keep := Classify(e, hasOrigin)
	return keep && (k == adapter.KindHuman || k == adapter.KindSummaryImport || k == adapter.KindSummaryCompaction)
}

func buildLine(es []Entry) (*line, error) {
	tip := tipOf(es)
	if tip == "" {
		return nil, ErrNodeNotFound
	}
	keep, err := Select(es, tip)
	if err != nil {
		return nil, err
	}
	byUUID := make(map[string]Entry, len(es))
	for _, e := range es {
		if u := e.UUID(); u != "" {
			byUUID[u] = e
		}
	}
	var chain []string
	seen := map[string]bool{}
	for cur := tip; cur != "" && keep[cur] && !seen[cur]; cur = byUUID[cur].ParentUUID() {
		seen[cur] = true
		chain = append(chain, cur)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	l := &line{es: es, keep: keep, chain: chain, turn: map[string]int{}}
	hasOrigin := HasHumanOrigin(es)
	t := 0
	for _, u := range chain {
		if opensTurn(byUUID[u], hasOrigin) {
			t++
		}
		l.turn[u] = t
	}
	l.last = t

	// What Select kept off the chain belongs to the turn of whatever kept it:
	// an assistant block to its requestId's turn (rule 2), an attachment or a
	// tool result to its parent's (rules 3 and 4). Iterated because a
	// recovered tool result can have attachment children of its own.
	reqTurn := map[string]int{}
	for _, u := range chain {
		if r := byUUID[u].RequestID(); r != "" {
			if _, ok := reqTurn[r]; !ok {
				reqTurn[r] = l.turn[u]
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, e := range es {
			u := e.UUID()
			if u == "" || !keep[u] {
				continue
			}
			if _, done := l.turn[u]; done {
				continue
			}
			if tn, ok := reqTurn[e.RequestID()]; ok && e.RequestID() != "" {
				l.turn[u] = tn
				changed = true
			} else if tn, ok := l.turn[e.ParentUUID()]; ok {
				l.turn[u] = tn
				changed = true
			}
		}
	}
	return l, nil
}

// span widens from..to, given in either order, to whole turns.
func (l *line) span(from, to string) (first, last int, err error) {
	a, okA := l.turn[from]
	b, okB := l.turn[to]
	if !okA || !okB {
		return 0, 0, ErrNotOnLine
	}
	if a > b {
		a, b = b, a
	}
	return a, b, nil
}

// firstOf is the chain entry that opens turn t; lastOf is its last chain entry.
func (l *line) firstOf(t int) string {
	for _, u := range l.chain {
		if l.turn[u] == t {
			return u
		}
	}
	return ""
}

// lastNodeOf is turn t's last entry that Entries makes a node of, in the
// same file order Entries walks.
func (l *line) lastNodeOf(t int) string {
	hasOrigin := HasHumanOrigin(l.es)
	out := ""
	for _, e := range l.es {
		u := e.UUID()
		if tn, ok := l.turn[u]; !ok || tn != t || !l.keep[u] {
			continue
		}
		if _, node := Classify(e, hasOrigin); node {
			out = u
		}
	}
	return out
}

func (l *line) lastOf(t int) string {
	out := ""
	for _, u := range l.chain {
		if l.turn[u] == t {
			out = u
		}
	}
	return out
}
