package claude

import (
	"errors"
	"strings"

	"herdr-tree/internal/adapter"
)

// ErrNothingLeft means a cut would remove every turn of the line.
var ErrNothingLeft = errors.New("a cut must leave at least one turn")

func parseForEdit(srcPath string) (*line, error) {
	es, skipped, err := ParseFile(srcPath)
	if err != nil {
		return nil, err
	}
	if skipped > 0 {
		return nil, ErrPartialTranscript
	}
	if err := checkVersion(es); err != nil {
		return nil, err
	}
	return buildLine(es)
}

// Widen reports what from..to covers once widened to whole turns.
func Widen(srcPath, from, to string) (adapter.Span, error) {
	l, err := parseForEdit(srcPath)
	if err != nil {
		return adapter.Span{}, err
	}
	a, b, err := l.span(from, to)
	if err != nil {
		return adapter.Span{}, err
	}
	return adapter.Span{First: a, Last: b, End: l.lastOf(b)}, nil
}

// Splice writes a new session holding srcPath's current line with e applied:
// the widened range dropped, e.Seed (if any) in its place, and the first
// entry after the range re-parented onto the seed, or onto the last entry
// before the range. Entry uuids are kept, so branches and cut markers that
// name them still resolve. The source is never modified.
func Splice(srcPath string, e adapter.Edit, dstCWD string) (adapter.Spliced, error) {
	if e.Seed != "" && !strings.HasPrefix(e.Seed, SummaryPrefix) && !strings.HasPrefix(e.Seed, CompactionPrefix) {
		return adapter.Spliced{}, ErrUnmarkedSeed
	}
	l, err := parseForEdit(srcPath)
	if err != nil {
		return adapter.Spliced{}, err
	}

	// a..b is the range of turns to drop. An insert is the empty range just
	// after the turn e.After belongs to.
	var a, b int
	if e.After != "" {
		t, ok := l.turn[e.After]
		if !ok {
			return adapter.Spliced{}, ErrNotOnLine
		}
		if e.Seed == "" {
			return adapter.Spliced{}, errors.New("an insert needs something to insert")
		}
		a, b = t+1, t
	} else if a, b, err = l.span(e.From, e.To); err != nil {
		return adapter.Spliced{}, err
	}
	if e.Seed == "" && a <= 1 && b >= l.last {
		return adapter.Spliced{}, ErrNothingLeft
	}

	var before, after string
	for _, u := range l.chain {
		switch t := l.turn[u]; {
		case t < a:
			before = u
		case t > b && after == "":
			after = u
		}
	}

	sid, err := newUUIDv4()
	if err != nil {
		return adapter.Spliced{}, err
	}
	joinTo := before
	var seedLine []byte
	if e.Seed != "" {
		seedUUID, err := newUUIDv4()
		if err != nil {
			return adapter.Spliced{}, err
		}
		if seedLine, err = Marshal(seedEntry(e.Seed, seedUUID, before, sid, dstCWD)); err != nil {
			return adapter.Spliced{}, err
		}
		joinTo = seedUUID
	}

	var buf []byte
	wroteSeed := false
	for _, en := range l.es {
		u := en.UUID()
		if u == "" || !l.keep[u] {
			continue // bookkeeping and everything off the line, as in GraftSeeded
		}
		if t := l.turn[u]; t >= a && t <= b {
			continue
		}
		m := rehome(en, sid, dstCWD)
		if u == after {
			if seedLine != nil {
				buf = append(append(buf, seedLine...), '\n')
				wroteSeed = true
			}
			if joinTo == "" {
				m["parentUuid"] = nil
			} else {
				m["parentUuid"] = joinTo
			}
		}
		enc, err := Marshal(Entry{Raw: m})
		if err != nil {
			return adapter.Spliced{}, err
		}
		buf = append(append(buf, enc...), '\n')
	}
	if seedLine != nil && !wroteSeed {
		buf = append(append(buf, seedLine...), '\n')
	}

	leaf := l.chain[len(l.chain)-1]
	if after == "" {
		leaf = joinTo
	}
	lp, err := Marshal(Entry{Raw: map[string]any{"type": "last-prompt", "leafUuid": leaf, "sessionId": sid}})
	if err != nil {
		return adapter.Spliced{}, err
	}
	buf = append(append(buf, lp...), '\n')

	if _, err := writeSession(dstCWD, sid, buf); err != nil {
		return adapter.Spliced{}, err
	}
	removed := b - max(a, 1) + 1
	if removed < 0 {
		removed = 0
	}
	return adapter.Spliced{SessionID: sid, Removed: removed, After: after}, nil
}
