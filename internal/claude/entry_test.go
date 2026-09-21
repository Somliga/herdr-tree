package claude

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestParseFileReadsEveryLine(t *testing.T) {
	es, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 11 {
		t.Fatalf("got %d entries want 11", len(es))
	}
	if es[0].Type() != "user" || es[0].UUID() != "u1" {
		t.Fatalf("first entry wrong: %s %s", es[0].Type(), es[0].UUID())
	}
	if es[1].RequestID() != "r1" {
		t.Fatalf("requestId not read: %q", es[1].RequestID())
	}
	if !es[4].HasToolUseResult() {
		t.Fatal("tr1 should report a toolUseResult")
	}
	if !es[8].IsSidechain() {
		t.Fatal("sc1 should be a sidechain")
	}
	if es[0].Version() != "2.1.278" {
		t.Fatalf("version %q", es[0].Version())
	}
	if es[10].AITitle() != "Fixture session" {
		t.Fatalf("aiTitle %q", es[10].AITitle())
	}
}

func TestTextExtraction(t *testing.T) {
	es, _ := ParseFile("testdata/simple.jsonl")
	if got := es[0].Text(); got != "first question" {
		t.Fatalf("got %q", got)
	}
	if got := es[6].Text(); got != "second question\nwith a second line" {
		t.Fatalf("got %q", got)
	}
}

func TestRoundTripPreservesUnknownFields(t *testing.T) {
	es, _ := ParseFile("testdata/simple.jsonl")
	b, err := Marshal(es[3]) // the attachment entry
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	att, ok := back["attachment"].(map[string]any)
	if !ok || att["kind"] != "reminder" {
		t.Fatalf("unknown field lost: %s", b)
	}
}

func TestLargeIntegersSurvive(t *testing.T) {
	line := []byte(`{"type":"x","big":1790002501047123456}`)
	e, err := parseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	// json.Marshal emits map keys in sorted order, so the bytes are NOT
	// identical to the input and must not be compared. What has to survive is
	// the integer's exact digits: without UseNumber it becomes a float64 and
	// loses precision.
	back, err := parseLine(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(back.Raw["big"]); got != "1790002501047123456" {
		t.Fatalf("integer corrupted: %s", got)
	}
	if _, ok := back.Raw["big"].(json.Number); !ok {
		t.Fatalf("want json.Number, got %T — UseNumber is not in effect", back.Raw["big"])
	}
}
