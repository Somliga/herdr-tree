package claude

import (
	"errors"
	"testing"
)

func mustLine(t *testing.T, path string) *line {
	t.Helper()
	es, skipped, err := ParseFile(path)
	if err != nil || skipped > 0 {
		t.Fatalf("parse %s: %v skipped=%d", path, err, skipped)
	}
	l, err := buildLine(es)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The tip is a conversation entry, never a sidechain or bookkeeping one.
// simple.jsonl ends with a sidechain prompt; splice.jsonl with a
// turn_duration entry. Either, taken as the tip, loses the real last reply.
func TestTipIsTheLastConversationEntry(t *testing.T) {
	for path, want := range map[string]string{
		"testdata/simple.jsonl": "a3",
		"testdata/splice.jsonl": "a4",
	} {
		es, _, err := ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := tipOf(es); got != want {
			t.Fatalf("%s: tip %q, want %q", path, got, want)
		}
	}
}

func TestTurnsFollowWhatTheUserTyped(t *testing.T) {
	l := mustLine(t, "testdata/splice.jsonl")
	want := map[string]int{
		"pre1": 0,
		"u1": 1, "a1": 1,
		"u2": 2, "a2": 2, "a2b": 2, "tr1": 2, "tr2": 2, "at2": 2, "a2c": 2,
		"u3": 3, "a3": 3,
		"u4": 4, "a4": 4,
	}
	for u, tn := range want {
		got, ok := l.turn[u]
		if !ok {
			t.Fatalf("%s has no turn: it was not kept", u)
		}
		if got != tn {
			t.Fatalf("%s in turn %d, want %d", u, got, tn)
		}
	}
	if len(l.turn) != len(want) {
		t.Fatalf("turn map has %d entries, want %d: something off the line was kept", len(l.turn), len(want))
	}
	if l.last != 4 {
		t.Fatalf("last turn %d, want 4", l.last)
	}
}

// The synthetic resume prompt is Claude Code's, not the user's: it must not
// open a turn, or a splice could cut between a tool call's turn and the
// resume that continued it.
func TestTheSyntheticResumeDoesNotOpenATurn(t *testing.T) {
	l := mustLine(t, "testdata/simple.jsonl")
	if l.turn["u2"] != 1 || l.turn["u3"] != 2 {
		t.Fatalf("u2 in turn %d, u3 in turn %d; want 1 and 2", l.turn["u2"], l.turn["u3"])
	}
}

func TestSpanWidensToWholeTurnsInEitherOrder(t *testing.T) {
	l := mustLine(t, "testdata/splice.jsonl")
	// a2b is a tool call off the chain in turn 2; a3 is a reply in turn 3.
	for _, c := range [][2]string{{"a2b", "a3"}, {"a3", "a2b"}} {
		a, b, err := l.span(c[0], c[1])
		if err != nil {
			t.Fatal(err)
		}
		if a != 2 || b != 3 {
			t.Fatalf("span(%s,%s) = %d..%d, want 2..3", c[0], c[1], a, b)
		}
	}
	if got := l.firstOf(2); got != "u2" {
		t.Fatalf("turn 2 opens at %q, want u2", got)
	}
	if got := l.lastOf(3); got != "a3" {
		t.Fatalf("turn 3 ends at %q, want a3", got)
	}
}

func TestSpanRefusesARewoundStretch(t *testing.T) {
	l := mustLine(t, "testdata/splice.jsonl")
	if _, _, err := l.span("x1", "a3"); !errors.Is(err, ErrNotOnLine) {
		t.Fatalf("err = %v, want ErrNotOnLine", err)
	}
}

// A native /compact summary was written by Claude Code, not typed, with or
// without origin stamped: it is preamble after the boundary (§3.4), never a
// turn and never a `user:` node.
func TestANativeCompactSummaryIsPreambleWithOrWithoutOrigin(t *testing.T) {
	for _, path := range []string{"testdata/compacted.jsonl", "testdata/compacted-preorigin.jsonl"} {
		es, _, err := ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range Entries(es) {
			if n.ID == "cs" {
				t.Fatalf("%s: the compact summary is a %v node", path, n.Kind)
			}
		}
		l := mustLine(t, path)
		want := map[string]int{"cb": 0, "cs": 0, "u3": 1, "a3": 1, "u4": 2, "a4": 2}
		for u, tn := range want {
			if got, ok := l.turn[u]; !ok || got != tn {
				t.Fatalf("%s: %s in turn %d (kept %v), want %d", path, u, got, ok, tn)
			}
		}
		if l.last != 2 {
			t.Fatalf("%s: last turn %d, want 2", path, l.last)
		}
	}
}
