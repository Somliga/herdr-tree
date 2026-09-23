# Context Editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user compact, cut, or insert into a line's context from the tree overlay, in one confirmed step, with the old line hidden and a live pane handed over.

**Architecture:** One new primitive, `claude.Splice`, rewrites a session's current line with a range of whole turns removed (optionally replaced by a seed) and everything after it re-attached. The store records `replaced_by`/`replaces`; `tree.Build` hides replaced sessions and resolves branch edges through them. The TUI gains a range menu (compact · cut) and a placement menu for `p` (insert · branch), and one command, `editCmd`, runs summarise → busy re-check → splice → save → open focused → close old pane.

**Tech Stack:** Go 1.27, Bubble Tea, the `herdr` CLI (shelled out), Claude Code JSONL transcripts.

**Spec:** `docs/superpowers/specs/2026-09-23-context-editing-design.md` — binding. Also read `docs/HANDOFF-2026-09-23.md` for standing constraints.

## Global Constraints

- Never modify a source transcript. Never delete a session. Grafted/spliced transcripts are written mode 0600, atomically (temp file + rename).
- Never log message content. A summary, a seed and a turn title are all message content. Every error that could carry a seed goes through `scrubbed(err, seed)` before reaching a status line.
- Do not touch the user's live Herdr, its panes, or `~/.config/herdr/`. Every test that could reach `herdr` puts `stubHerdr` first on `PATH`. Never run the real `herdr` or `claude` binaries. Never run `scripts/verify-*.sh` or `cmd/timelinecheck` — they spend API budget.
- A test must be able to fail. Fixtures are shaped like real transcripts (see `internal/claude/testdata/parallel.jsonl`). When mutating to check a test, mutate to a live-but-wrong expression; a mutation that fails to compile proves nothing.
- `internal/tui` must not import `internal/claude`.
- Commit after every task, message style `feat: …`/`test: …`/`fix: …` in plain words, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.
- Run `go vet ./... && go test ./...` before every commit; both must be clean.

---

## File map

| File | Change |
|---|---|
| `internal/adapter/adapter.go` | `Edit`, `Span`, `Spliced` types; `Widen`, `Splice` methods; `Resume` gains `focus bool` |
| `internal/claude/line.go` (new) | turn model of a transcript's current line: `tipOf`, `buildLine`, `opensTurn`, `span`, `firstOf`, `lastOf` |
| `internal/claude/splice.go` (new) | `Widen`, `Splice` |
| `internal/claude/graft.go` | factor `rehome`, `seedEntry`, `writeSession` out of `GraftSeeded` |
| `internal/claude/summarise.go` | widen the range; graft at the end turn's last entry |
| `internal/claude/adapter.go` | wire `Widen`, `Splice`; `Resume(…, focus)` |
| `internal/claude/testdata/splice.jsonl` (new) | preamble, parallel tools, attachment, rewound stretch |
| `internal/herdr/herdr.go` | `Split(cwd, focus)`, `AgentState`, `ClosePane` |
| `internal/store/store.go` | `ReplacedBy`, `Replaces`, `Kind`, `Cut`; `Replace`, `Resolve`; one-way merge |
| `internal/tree/tree.go` | hide replaced sessions; resolve edges; `FromRemoved`, `CutHere`, `CutAfter` |
| `internal/tui/view.go` | menus, confirmations, `editCmd`, markers, footer |
| `internal/tui/edit.go` (new) | `editOp`, `editCmd`, the three confirm builders — keeps `view.go` from growing further |
| `cmd/herdr-tree/main.go` | pass `herdr.AgentState`, `herdr.ClosePane` into `tui.Run` |
| `cmd/splicecheck/main.go` (new) | read-only check over local transcripts |

---

### Task 1: The turn model of a line, and widening

**Files:**
- Create: `internal/claude/line.go`, `internal/claude/line_test.go`, `internal/claude/testdata/splice.jsonl`
- Modify: `internal/claude/summarise.go` (graft point and titles)
- Test: `internal/claude/line_test.go`, existing `internal/claude/summarise_test.go`

**Interfaces:**
- Produces (package-internal, used by Task 2):
  - `type line struct { es []Entry; keep map[string]bool; chain []string; turn map[string]int; last int }`
  - `func tipOf(es []Entry) string`
  - `func buildLine(es []Entry) (*line, error)`
  - `func (l *line) span(from, to string) (first, last int, err error)`
  - `func (l *line) firstOf(t int) string`, `func (l *line) lastOf(t int) string`
  - `var ErrNotOnLine`

- [ ] **Step 1: Write the fixture**

`internal/claude/testdata/splice.jsonl` — a preamble attachment, four turns, a parallel tool call in turn 2 with an off-chain sibling block and result, an attachment in turn 2, and an abandoned (rewound) stretch `x1`/`xa1` hanging off `a1`:

```
{"type":"attachment","uuid":"pre1","parentUuid":null,"sessionId":"S","cwd":"/repo","version":"2.1.278","timestamp":"2026-01-01T12:00:00Z","attachment":{"kind":"reminder"}}
{"type":"user","uuid":"u1","parentUuid":"pre1","sessionId":"S","cwd":"/repo","version":"2.1.278","timestamp":"2026-01-01T12:00:01Z","message":{"role":"user","content":[{"type":"text","text":"one"}]}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"S","requestId":"r1","timestamp":"2026-01-01T12:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"reply one"}]}}
{"type":"user","uuid":"x1","parentUuid":"a1","sessionId":"S","timestamp":"2026-01-01T12:00:03Z","message":{"role":"user","content":[{"type":"text","text":"abandoned"}]}}
{"type":"assistant","uuid":"xa1","parentUuid":"x1","sessionId":"S","requestId":"rx","timestamp":"2026-01-01T12:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"abandoned reply"}]}}
{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"S","timestamp":"2026-01-01T12:00:05Z","message":{"role":"user","content":[{"type":"text","text":"two"}]}}
{"type":"assistant","uuid":"a2","parentUuid":"u2","sessionId":"S","requestId":"r2","timestamp":"2026-01-01T12:00:06Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}
{"type":"assistant","uuid":"a2b","parentUuid":"a2","sessionId":"S","requestId":"r2","timestamp":"2026-01-01T12:00:07Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash"}]}}
{"type":"user","uuid":"tr1","parentUuid":"a2","sessionId":"S","timestamp":"2026-01-01T12:00:08Z","toolUseResult":{"ok":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]}}
{"type":"user","uuid":"tr2","parentUuid":"a2b","sessionId":"S","timestamp":"2026-01-01T12:00:09Z","toolUseResult":{"ok":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2"}]}}
{"type":"attachment","uuid":"at2","parentUuid":"u2","sessionId":"S","timestamp":"2026-01-01T12:00:10Z","attachment":{"kind":"reminder"}}
{"type":"assistant","uuid":"a2c","parentUuid":"tr1","sessionId":"S","requestId":"r3","timestamp":"2026-01-01T12:00:11Z","message":{"role":"assistant","content":[{"type":"text","text":"reply two"}]}}
{"type":"user","uuid":"u3","parentUuid":"a2c","sessionId":"S","timestamp":"2026-01-01T12:00:12Z","message":{"role":"user","content":[{"type":"text","text":"three"}]}}
{"type":"assistant","uuid":"a3","parentUuid":"u3","sessionId":"S","requestId":"r4","timestamp":"2026-01-01T12:00:13Z","message":{"role":"assistant","content":[{"type":"text","text":"reply three"}]}}
{"type":"user","uuid":"u4","parentUuid":"a3","sessionId":"S","timestamp":"2026-01-01T12:00:14Z","message":{"role":"user","content":[{"type":"text","text":"four"}]}}
{"type":"assistant","uuid":"a4","parentUuid":"u4","sessionId":"S","requestId":"r5","timestamp":"2026-01-01T12:00:15Z","message":{"role":"assistant","content":[{"type":"text","text":"reply four"}]}}
{"type":"system","subtype":"turn_duration","uuid":"sd4","parentUuid":"a4","sessionId":"S","timestamp":"2026-01-01T12:00:16Z"}
{"type":"last-prompt","leafUuid":"a4","sessionId":"S"}
```

The trailing `system/turn_duration` entry is there on purpose: 732 of them sit on real tip chains, and the tip must not be taken from one.

- [ ] **Step 2: Write the failing tests**

`internal/claude/line_test.go`:

```go
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
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./internal/claude/ -run 'TestTip|TestTurns|TestTheSynthetic|TestSpan' -v`
Expected: FAIL to compile, `undefined: buildLine`.

- [ ] **Step 4: Write `internal/claude/line.go`**

```go
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

func (l *line) lastOf(t int) string {
	out := ""
	for _, u := range l.chain {
		if l.turn[u] == t {
			out = u
		}
	}
	return out
}
```

- [ ] **Step 5: Run the new tests**

Run: `go test ./internal/claude/ -run 'TestTip|TestTurns|TestTheSynthetic|TestSpan' -v`
Expected: PASS.

- [ ] **Step 6: Write the failing Summarise test**

Summarising "up to u3" in v2 grafted AT u3, so the model never saw u3's reply `a3`. Widened, it reads the whole end turn. Add to `internal/claude/summarise_test.go`, using the existing `stubClaude` helper (read it first; it records argv to a file and prints a canned summary):

```go
// The end of a range is a whole turn: summarising up to a prompt must let the
// model read that prompt's reply. v2 grafted at the prompt itself and cut the
// reply off.
func TestSummariseReadsTheWholeEndTurn(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	var grafted string
	summariseGraftHook = func(path string) {
		es, _, _ := ParseFile(path)
		for _, e := range es {
			if e.UUID() == "a3" {
				grafted = "a3"
			}
		}
	}
	defer func() { summariseGraftHook = nil }()
	stubClaude(t, "echo summary")
	if _, err := Summarise("testdata/simple.jsonl", "u1", "u3", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if grafted != "a3" {
		t.Fatal("the throwaway session stops before the end turn's reply")
	}
}
```

If `stubClaude`'s signature differs from `stubClaude(t, script string)`, adapt the call to match it — do not change the helper.

- [ ] **Step 7: Run it to see it fail**

Run: `go test ./internal/claude/ -run TestSummariseReadsTheWholeEndTurn -v`
Expected: FAIL to compile, `undefined: summariseGraftHook`.

- [ ] **Step 8: Change `Summarise`**

In `internal/claude/summarise.go`, add at package level:

```go
// summariseGraftHook, when set, sees the throwaway transcript before it is
// resumed. Tests only: it is how a test observes what the model is shown.
var summariseGraftHook func(path string)
```

Replace the block from `var titles []string` through `sid, path, err := Graft(srcPath, toTurn, tmpCWD)` with:

```go
	var titles []string
	title := func(id string) string {
		for _, e := range es {
			if e.UUID() == id {
				return Title(e.Text(), 60)
			}
		}
		return id
	}

	// Whole turns, as a splice removes them: the model reads through the end
	// turn's last entry, and the prompt names each end by the turn's opening
	// entry — the thing the user typed.
	l, err := buildLine(es)
	if err != nil {
		return "", err
	}
	a, b, err := l.span(fromTurn, toTurn)
	if err != nil {
		return "", err
	}
	fromTurn, toTurn = l.firstOf(a), l.firstOf(b)

	sid, path, err := Graft(srcPath, l.lastOf(b), tmpCWD)
	if err != nil {
		return "", err
	}
	if summariseGraftHook != nil {
		summariseGraftHook(path)
	}
```

Leave the rest (`fromTitle, toTitle := title(fromTurn), title(toTurn)` onward) unchanged; it now titles the opening entries.

- [ ] **Step 9: Run the package**

Run: `go test ./internal/claude/ -v 2>&1 | tail -30`
Expected: PASS, including every existing `TestSummarise*`.

- [ ] **Step 10: Commit**

```bash
go vet ./... && go test ./...
git add internal/claude/line.go internal/claude/line_test.go internal/claude/testdata/splice.jsonl internal/claude/summarise.go internal/claude/summarise_test.go
git commit -m "feat: divide a line into whole turns, and summarise through the end turn

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `Splice`

**Files:**
- Create: `internal/claude/splice.go`, `internal/claude/splice_test.go`
- Modify: `internal/claude/graft.go` (factor helpers out of `GraftSeeded`), `internal/adapter/adapter.go`, `internal/claude/adapter.go`, `internal/tui/view_test.go` (`fakeAdapter` gains the new methods)

**Interfaces:**
- Consumes: Task 1's `line`, `buildLine`, `span`, `lastOf`, `ErrNotOnLine`.
- Produces:
  - `adapter.Edit{From, To, After, Seed string}`
  - `adapter.Span{First, Last int; End string}`
  - `adapter.Spliced{SessionID string; Removed int; After string}`
  - `Adapter.Widen(src Session, fromNode, toNode string) (Span, error)`
  - `Adapter.Splice(src Session, e Edit, dstCWD string) (Spliced, error)`
  - `claude.Widen(srcPath, from, to string) (adapter.Span, error)`
  - `claude.Splice(srcPath string, e adapter.Edit, dstCWD string) (adapter.Spliced, error)`
  - `claude.ErrNothingLeft`
  - Helpers in `graft.go`: `rehome(e Entry, sid, cwd string) map[string]any`, `seedEntry(seed, uuid, parent, sid, cwd string) Entry`, `writeSession(dstCWD, sid string, buf []byte) (string, error)`

- [ ] **Step 1: Factor the helpers out of `GraftSeeded`**

In `internal/claude/graft.go`, add:

```go
// rehome copies an entry into a new session: session id and cwd rewritten,
// every other top-level key verbatim. Shallow — nested maps (message,
// attachment, toolUseResult) still alias the source, so never mutate inside
// them.
func rehome(e Entry, sid, cwd string) map[string]any {
	m := make(map[string]any, len(e.Raw))
	for k, v := range e.Raw {
		m[k] = v
	}
	for _, k := range []string{"sessionId", "session_id"} {
		if _, ok := m[k]; ok {
			m[k] = sid
		}
	}
	if _, ok := m["cwd"]; ok {
		m["cwd"] = cwd
	}
	return m
}

// seedEntry is the one user entry we write ourselves. parent "" makes it a
// root. The herdrTree kind is DERIVED from the text prefix, which is what
// everything else reads.
func seedEntry(seed, uuid, parent, sid, cwd string) Entry {
	var p any
	if parent != "" {
		p = parent
	}
	kind := "compaction"
	if strings.HasPrefix(seed, SummaryPrefix) {
		kind = "summary"
	}
	return Entry{Raw: map[string]any{
		"type": "user", "uuid": uuid, "parentUuid": p,
		"sessionId": sid, "cwd": cwd,
		"version":   verifiedMajorMinor + ".0",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"userType":  "external", "isSidechain": false,
		"message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "text", "text": seed}}},
		"herdrTree": map[string]any{"kind": kind},
	}}
}

// writeSession writes buf as a new transcript for dstCWD, mode 0600,
// atomically, and returns its path.
func writeSession(dstCWD, sid string, buf []byte) (string, error) {
	dstDir := filepath.Join(ProjectsDir(), SlugFor(dstCWD))
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return "", err
	}
	dstPath := filepath.Join(dstDir, sid+".jsonl")
	tmp, err := os.CreateTemp(dstDir, ".graft-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return dstPath, os.Rename(tmpName, dstPath)
}
```

Then in `GraftSeeded`: replace the per-entry shallow-copy/rewrite block with `m := rehome(e, newSessionID, dstCWD)`; replace the seed `raw` construction and its `herdrTree` kind block with `Marshal(seedEntry(seed, seedUUID, atNode, newSessionID, dstCWD))`; replace everything from `dstDir := …`/`MkdirAll` and from `tmp, err := os.CreateTemp` to the final rename with one call `dstPath, err = writeSession(dstCWD, newSessionID, buf)` placed after the `last-prompt` line is appended. Keep every existing comment that still describes the code it sits on (the uuid-less bookkeeping comment stays in `GraftSeeded`'s loop).

- [ ] **Step 2: Prove the refactor changed nothing**

Run: `go test ./internal/claude/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: `ok`. Every `TestGraft*` passes unchanged.

- [ ] **Step 3: Add the adapter types and methods**

In `internal/adapter/adapter.go`, after `Pane`:

```go
// Edit is one change to a line's context. From..To names a range of entries,
// widened to whole turns, to remove; Seed, if set, takes its place. After,
// used instead of a range, names an entry after whose turn Seed is inserted
// and nothing is removed. A range with no Seed is a cut.
type Edit struct {
	From, To string
	After    string
	Seed     string
}

// Span is a selection widened to whole turns.
type Span struct {
	First, Last int    // turn numbers; 0 is the preamble before the first prompt
	End         string // the last entry of turn Last: where a summary stops reading
}

// Spliced is what a splice wrote.
type Spliced struct {
	SessionID string
	Removed   int    // whole turns removed, not counting the preamble
	After     string // the first entry after the edit, "" when nothing follows
}
```

And in the `Adapter` interface, after `Summarise`:

```go
	// Widen reports what a range covers once widened to whole turns.
	Widen(src Session, fromNode, toNode string) (Span, error)
	// Splice writes a new session with e applied to src's current line. The
	// source is never modified.
	Splice(src Session, e Edit, dstCWD string) (Spliced, error)
```

In `internal/tui/view_test.go`, extend `fakeAdapter` so the package compiles:

```go
	// in the struct:
	span      adapter.Span
	spliceErr error
	spliced   []adapter.Edit
```

```go
func (f *fakeAdapter) Widen(adapter.Session, string, string) (adapter.Span, error) {
	return f.span, nil
}
func (f *fakeAdapter) Splice(_ adapter.Session, e adapter.Edit, _ string) (adapter.Spliced, error) {
	if f.spliceErr != nil {
		return adapter.Spliced{}, f.spliceErr
	}
	f.spliced = append(f.spliced, e)
	return adapter.Spliced{SessionID: "spliced-sid", Removed: 2, After: "t3"}, nil
}
```

In `internal/claude/adapter.go`:

```go
func (claudeAdapter) Widen(src adapter.Session, fromNode, toNode string) (adapter.Span, error) {
	return Widen(sourcePath(src), fromNode, toNode)
}

func (claudeAdapter) Splice(src adapter.Session, e adapter.Edit, dstCWD string) (adapter.Spliced, error) {
	return Splice(sourcePath(src), e, dstCWD)
}
```

- [ ] **Step 4: Write the failing splice tests**

`internal/claude/splice_test.go`:

```go
package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-tree/internal/adapter"
)

const seedText = CompactionPrefix + " u2..u3\n\nturns two and three, briefly"

// spliced runs Splice into a fresh projects dir and returns the new
// transcript's entries keyed by uuid, plus its uuids in file order.
func spliced(t *testing.T, src string, e adapter.Edit) (adapter.Spliced, map[string]Entry, []string) {
	t.Helper()
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	before, _ := os.ReadFile(src)
	res, err := Splice(src, e, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(src)
	if string(before) != string(after) {
		t.Fatal("splicing modified the source transcript")
	}
	path := filepath.Join(ProjectsDir(), "*", res.SessionID+".jsonl")
	m, _ := filepath.Glob(path)
	if len(m) != 1 {
		t.Fatalf("want one spliced file, found %v", m)
	}
	fi, _ := os.Stat(m[0])
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("spliced file mode %v, want 0600", fi.Mode().Perm())
	}
	es, skipped, err := ParseFile(m[0])
	if err != nil || skipped > 0 {
		t.Fatalf("spliced file does not parse: %v skipped=%d", err, skipped)
	}
	by := map[string]Entry{}
	var order []string
	for _, e := range es {
		if u := e.UUID(); u != "" {
			if e.SessionID() != res.SessionID {
				t.Fatalf("%s carries session %q, want %q", u, e.SessionID(), res.SessionID)
			}
			by[u] = e
			order = append(order, u)
		}
	}
	return res, by, order
}

func seedOf(t *testing.T, by map[string]Entry) Entry {
	t.Helper()
	for _, e := range by {
		if strings.HasPrefix(e.Text(), "⤶") {
			return e
		}
	}
	t.Fatal("no seed entry in the spliced file")
	return Entry{}
}

func parent(e Entry) string { return e.ParentUUID() }

func TestCutRemovesWholeTurnsAndRejoinsTheRest(t *testing.T) {
	res, by, order := spliced(t, "testdata/splice.jsonl", adapter.Edit{From: "a2b", To: "a3"})
	want := []string{"pre1", "u1", "a1", "u4", "a4"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("kept %v, want %v", order, want)
	}
	if parent(by["u4"]) != "a1" {
		t.Fatalf("u4 rejoined to %q, want a1", parent(by["u4"]))
	}
	if res.Removed != 2 || res.After != "u4" {
		t.Fatalf("result %+v, want Removed 2, After u4", res)
	}
}

func TestCompactPutsTheSeedWhereTheRangeWas(t *testing.T) {
	_, by, order := spliced(t, "testdata/splice.jsonl", adapter.Edit{From: "u2", To: "u3", Seed: seedText})
	seed := seedOf(t, by)
	if parent(seed) != "a1" {
		t.Fatalf("seed parented to %q, want a1", parent(seed))
	}
	if parent(by["u4"]) != seed.UUID() {
		t.Fatalf("u4 rejoined to %q, want the seed", parent(by["u4"]))
	}
	for _, gone := range []string{"u2", "a2", "a2b", "tr1", "tr2", "at2", "a2c", "u3", "a3"} {
		if _, ok := by[gone]; ok {
			t.Fatalf("%s survived the compaction", gone)
		}
	}
	// File order puts the seed immediately before what follows it.
	for i, u := range order {
		if u == "u4" && order[i-1] != seed.UUID() {
			t.Fatalf("seed not written just before u4: %v", order)
		}
	}
}

func TestInsertKeepsEverythingAfter(t *testing.T) {
	seed := SummaryPrefix + " other\n\nwhat the branch found"
	res, by, _ := spliced(t, "testdata/splice.jsonl", adapter.Edit{After: "a1", Seed: seed})
	s := seedOf(t, by)
	if parent(s) != "a1" || parent(by["u2"]) != s.UUID() {
		t.Fatalf("seed under %q, u2 under %q; want a1 and the seed", parent(s), parent(by["u2"]))
	}
	for _, kept := range []string{"u2", "a2", "a2b", "tr1", "tr2", "at2", "a2c", "u3", "a3", "u4", "a4"} {
		if _, ok := by[kept]; !ok {
			t.Fatalf("%s was lost by an insert", kept)
		}
	}
	if res.Removed != 0 {
		t.Fatalf("an insert removed %d turns", res.Removed)
	}
}

// Inserting "after" a mid-turn entry lands after the WHOLE turn: anything
// else would separate a tool call from its result.
func TestInsertAfterAToolCallLandsAfterItsTurn(t *testing.T) {
	_, by, _ := spliced(t, "testdata/splice.jsonl", adapter.Edit{After: "a2b", Seed: seedText})
	s := seedOf(t, by)
	if parent(s) != "a2c" || parent(by["u3"]) != s.UUID() {
		t.Fatalf("seed under %q, u3 under %q; want a2c and the seed", parent(s), parent(by["u3"]))
	}
}

func TestCompactingTheFirstTurnKeepsThePreamble(t *testing.T) {
	_, by, _ := spliced(t, "testdata/splice.jsonl", adapter.Edit{From: "u1", To: "a1", Seed: seedText})
	if parent(seedOf(t, by)) != "pre1" {
		t.Fatalf("seed parented to %q, want the preamble's pre1", parent(seedOf(t, by)))
	}
}

func TestWithNoPreambleTheSeedBecomesTheRoot(t *testing.T) {
	_, by, _ := spliced(t, "testdata/parallel.jsonl", adapter.Edit{From: "u1", To: "a3", Seed: seedText})
	s := seedOf(t, by)
	if s.Raw["parentUuid"] != nil {
		t.Fatalf("seed parent %v, want null", s.Raw["parentUuid"])
	}
	if parent(by["u2"]) != s.UUID() {
		t.Fatal("u2 not rejoined to the root seed")
	}
}

func TestACutFromTheFirstTurnMakesTheNextTurnTheRoot(t *testing.T) {
	_, by, _ := spliced(t, "testdata/parallel.jsonl", adapter.Edit{From: "u1", To: "a1"})
	if by["u2"].Raw["parentUuid"] != nil {
		t.Fatalf("u2 parent %v, want null", by["u2"].Raw["parentUuid"])
	}
}

func TestCompactingEverythingLeavesOnlyTheSeed(t *testing.T) {
	_, by, _ := spliced(t, "testdata/parallel.jsonl", adapter.Edit{From: "u1", To: "u2", Seed: seedText})
	if len(by) != 1 {
		t.Fatalf("kept %d entries, want only the seed", len(by))
	}
}

// The leaf pointer names the tip when something follows the edit, and the
// seed when the edit reached the end.
func TestTheLeafPointerFollowsTheEdit(t *testing.T) {
	for _, c := range []struct {
		e    adapter.Edit
		want func(map[string]Entry) string
	}{
		{adapter.Edit{From: "u2", To: "u3", Seed: seedText}, func(map[string]Entry) string { return "a4" }},
		{adapter.Edit{From: "u3", To: "a4", Seed: seedText}, func(by map[string]Entry) string { return seedOf(t, by).UUID() }},
	} {
		res, by, _ := spliced(t, "testdata/splice.jsonl", c.e)
		path, _ := filepath.Glob(filepath.Join(ProjectsDir(), "*", res.SessionID+".jsonl"))
		es, _, _ := ParseFile(path[0])
		leaf := ""
		for _, e := range es {
			if e.Type() == "last-prompt" {
				leaf, _ = e.Raw["leafUuid"].(string)
			}
		}
		if leaf != c.want(by) {
			t.Fatalf("%+v: leaf %q, want %q", c.e, leaf, c.want(by))
		}
	}
}

func TestSpliceRefuses(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	for name, c := range map[string]struct {
		e    adapter.Edit
		want error
	}{
		"a cut of every turn":    {adapter.Edit{From: "u1", To: "a4"}, ErrNothingLeft},
		"a rewound stretch":      {adapter.Edit{From: "x1", To: "xa1"}, ErrNotOnLine},
		"an unmarked seed":       {adapter.Edit{From: "u2", To: "u3", Seed: "no prefix"}, ErrUnmarkedSeed},
	} {
		if _, err := Splice("testdata/splice.jsonl", c.e, t.TempDir()); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(ProjectsDir(), "*", "*.jsonl")); len(left) != 0 {
		t.Fatalf("a refused splice wrote %v", left)
	}
}

// Splice reads the source when it runs, not when the range was chosen: turns
// the agent added in between are kept, after the edit.
func TestTurnsAddedSinceTheRangeWasChosenAreKept(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	b, _ := os.ReadFile("testdata/splice.jsonl")
	b = append(b, []byte(`{"type":"user","uuid":"u5","parentUuid":"a4","sessionId":"S","timestamp":"2026-01-01T12:01:00Z","message":{"role":"user","content":[{"type":"text","text":"five"}]}}`+"\n"+
		`{"type":"assistant","uuid":"a5","parentUuid":"u5","sessionId":"S","requestId":"r6","timestamp":"2026-01-01T12:01:01Z","message":{"role":"assistant","content":[{"type":"text","text":"reply five"}]}}`+"\n")...)
	if err := os.WriteFile(src, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_, by, _ := spliced(t, src, adapter.Edit{From: "u2", To: "u3"})
	if parent(by["u5"]) != "a4" || parent(by["a5"]) != "u5" {
		t.Fatal("turns appended after the range was chosen were not kept in place")
	}
}

func TestWidenReportsTurnNumbersAndTheReadingEnd(t *testing.T) {
	s, err := Widen("testdata/splice.jsonl", "a3", "a2b")
	if err != nil {
		t.Fatal(err)
	}
	if s.First != 2 || s.Last != 3 || s.End != "a3" {
		t.Fatalf("span %+v, want 2..3 ending a3", s)
	}
}
```

- [ ] **Step 5: Run to see them fail**

Run: `go test ./internal/claude/ -run 'TestCut|TestCompact|TestInsert|TestWith|TestACut|TestTheLeaf|TestSpliceRefuses|TestTurnsAdded|TestWiden' -v`
Expected: FAIL to compile, `undefined: Splice`.

- [ ] **Step 6: Write `internal/claude/splice.go`**

```go
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
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/claude/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: `ok`.

- [ ] **Step 8: Mutation check — the re-parent**

Change `m["parentUuid"] = joinTo` to `m["parentUuid"] = before`. Run `go test ./internal/claude/ -run 'TestCompact|TestInsert'`. Expected: FAIL. Revert. Then change `case t < a:` to `case t <= a:`. Run `go test ./internal/claude/ -run 'TestCut|TestCompact'`. Expected: FAIL. Revert. Record both in the commit message body.

- [ ] **Step 9: Commit**

```bash
go vet ./... && go test ./...
git add internal/adapter/adapter.go internal/claude/ internal/tui/view_test.go
git commit -m "feat: splice a line — cut, compact or insert whole turns, keeping what follows

Mutations checked: rejoin to 'before' instead of the seed, and an
off-by-one range start; both turn the suite red.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: The store records replacement

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces:
  - `store.Branch` fields: `ReplacedBy string`, `Replaces string`, `Kind string`, `Cut *Cut`
  - `type Cut struct { Turns int; At string }`
  - `func (s *Store) Replace(oldSID, newSID string, b Branch)`
  - `func (s *Store) Resolve(sessionID string) string`
  - Kind constants: `KindCompacted = "compacted"`, `KindCut = "cut"`, `KindInserted = "inserted"`

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/store_test.go` (read the file first for its existing `Load` setup — it sets `HERDR_PLUGIN_CONFIG_DIR` to a temp dir; reuse that pattern):

```go
func TestReplaceHidesTheOldLineAndPutsTheNewOneInItsPlace(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Add("old", Branch{GraftedFrom: From{SessionID: "trunk", Node: "t4"}, Title: "b"})
	s.Replace("old", "new", Branch{Kind: KindCompacted, Title: "⤶ x"})

	if s.Branches["old"].ReplacedBy != "new" {
		t.Fatal("old is not marked replaced")
	}
	nb := s.Branches["new"]
	if nb.Replaces != "old" || nb.Kind != KindCompacted {
		t.Fatalf("new record %+v", nb)
	}
	if nb.GraftedFrom != (From{SessionID: "trunk", Node: "t4"}) {
		t.Fatalf("new line does not hang where the old one did: %+v", nb.GraftedFrom)
	}
}

func TestReplacingALineWithNoRecordCreatesOne(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Replace("root-sess", "new", Branch{Kind: KindCut})
	if s.Branches["root-sess"].ReplacedBy != "new" {
		t.Fatal("no record was created for the replaced root session")
	}
	if s.Branches["new"].GraftedFrom.SessionID != "" {
		t.Fatal("a replaced root must stay a root")
	}
}

func TestResolveFollowsReplacementsAndSurvivesACycle(t *testing.T) {
	s := &Store{Branches: map[string]Branch{
		"a": {ReplacedBy: "b"}, "b": {ReplacedBy: "c"},
		"x": {ReplacedBy: "y"}, "y": {ReplacedBy: "x"},
	}}
	if got := s.Resolve("a"); got != "c" {
		t.Fatalf("Resolve(a) = %q, want c", got)
	}
	if got := s.Resolve("q"); got != "q" {
		t.Fatalf("Resolve of an unknown session = %q, want itself", got)
	}
	s.Resolve("x") // must return, not hang
}

// A second overlay that loaded before the replacement and saves something
// unrelated must not un-hide the old line.
func TestSaveNeverClearsAReplacement(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s1, _ := Load("/repo")
	s1.Add("old", Branch{Title: "b"})
	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}
	stale, _ := Load("/repo")

	s1.Replace("old", "new", Branch{Kind: KindCut})
	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}
	stale.SetLabel("old", "t1", "landmark")
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	after, _ := Load("/repo")
	if after.Branches["old"].ReplacedBy != "new" {
		t.Fatal("a stale save cleared replaced_by")
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/store/ -v 2>&1 | tail -20`
Expected: FAIL to compile, `s.Replace undefined`.

- [ ] **Step 3: Implement**

In `internal/store/store.go`, extend `Branch`:

```go
type Branch struct {
	GraftedFrom From      `json:"grafted_from"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	Artifacts   []string  `json:"artifacts"` // always [], populated in v1.1

	// A splice writes a new session and retires the old one. The old
	// record gets ReplacedBy; the new one Replaces, Kind, and for a cut,
	// Cut. The transcript of a replaced session stays on disk.
	ReplacedBy string `json:"replaced_by,omitempty"`
	Replaces   string `json:"replaces,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Cut        *Cut   `json:"cut,omitempty"`
}

// Cut is what the tree needs to mark a removed stretch: nothing about a cut
// is left in the transcript itself.
type Cut struct {
	Turns int    `json:"turns"`
	At    string `json:"at"` // first entry after the cut; "" when nothing follows
}

const (
	KindCompacted = "compacted"
	KindCut       = "cut"
	KindInserted  = "inserted"
)
```

Add after `Add`:

```go
// Replace records that newSID replaces oldSID. The new line takes the old
// one's place — including where it hung, so a compacted branch stays under
// the trunk turn it left.
func (s *Store) Replace(oldSID, newSID string, b Branch) {
	old := s.Branches[oldSID]
	old.ReplacedBy = newSID
	s.Add(oldSID, old)
	b.GraftedFrom = old.GraftedFrom
	b.Replaces = oldSID
	s.Add(newSID, b)
}

// Resolve follows replaced_by from sessionID to the line that now stands in
// its place, through any number of splices.
func (s *Store) Resolve(sessionID string) string {
	seen := map[string]bool{}
	for !seen[sessionID] {
		seen[sessionID] = true
		next := s.Branches[sessionID].ReplacedBy
		if next == "" {
			return sessionID
		}
		sessionID = next
	}
	return sessionID // a cycle in a hand-edited store: stop where it closed
}
```

In `Save`, replace the branch-merge loop with:

```go
		for id, b := range onDisk.Branches {
			ours, ok := s.Branches[id]
			if !ok {
				s.Branches[id] = b
				continue
			}
			// Replacement is one-way: a store loaded before it must not
			// clear it by saving something unrelated.
			if ours.ReplacedBy == "" && b.ReplacedBy != "" {
				ours.ReplacedBy = b.ReplacedBy
				s.Branches[id] = ours
			}
		}
```

Update the doc comment above `Save`: replace "v1 never deletes a branch, so a merge can never resurrect something intentionally removed." with "Nothing deletes a branch, and replaced_by is never cleared, so a merge can never resurrect something intentionally removed."

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/store/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: `ok`.

- [ ] **Step 5: Mutation check**

Delete the condition's second half so it reads `if ours.ReplacedBy == "" && false {`. Run `go test ./internal/store/ -run TestSaveNeverClearsAReplacement`. Expected: FAIL. Revert.

- [ ] **Step 6: Commit**

```bash
go vet ./... && go test ./...
git add internal/store/
git commit -m "feat: the store records which line replaced which, one-way

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: The tree hides replaced lines and re-attaches branches

**Files:**
- Modify: `internal/tree/tree.go`, `internal/tui/view.go` (`renderRow` markers)
- Test: `internal/tree/tree_test.go`, `internal/tui/view_test.go`

**Interfaces:**
- Consumes: Task 3's `Branch.ReplacedBy`, `Branch.Cut`, `Store.Resolve`.
- Produces: `tree.Node` fields `FromRemoved bool`, `CutHere int`, `CutAfter int`.

- [ ] **Step 1: Write the failing tree tests**

Append to `internal/tree/tree_test.go` (read it first; reuse its session-building helpers if they exist, otherwise use this one):

```go
func sess(id string, turns ...string) adapter.Session {
	s := adapter.Session{ID: id, CWD: "/repo"}
	for _, t := range turns {
		s.Nodes = append(s.Nodes, adapter.Node{ID: t, Title: t, Kind: adapter.KindHuman})
	}
	return s
}

func sessionsIn(roots []*Node) map[string]bool {
	out := map[string]bool{}
	var walk func(n *Node)
	walk = func(n *Node) {
		out[n.SessionID] = true
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

func TestAReplacedLineIsHiddenAndItsReplacementShown(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut})
	roots := Build([]adapter.Session{sess("new", "t1", "t4"), sess("old", "t1", "t2", "t3", "t4")}, st)
	got := sessionsIn(roots)
	if got["old"] || !got["new"] {
		t.Fatalf("sessions shown %v, want new and not old", got)
	}
}

// If the replacement's transcript is gone, hiding the old line would lose the
// conversation from view entirely.
func TestAReplacedLineShowsAgainIfItsReplacementIsGone(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut})
	roots := Build([]adapter.Session{sess("old", "t1", "t2")}, st)
	if !sessionsIn(roots)["old"] {
		t.Fatal("old line hidden though nothing replaces it on disk")
	}
}

func TestABranchOffAReplacedLineReattachesToTheSameTurn(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{
		"br": {GraftedFrom: store.From{SessionID: "old", Node: "t4"}},
	}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCompacted})
	roots := Build([]adapter.Session{
		sess("new", "t1", "seed", "t4"), sess("old", "t1", "t2", "t3", "t4"), sess("br", "b1"),
	}, st)
	var t4 *Node
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "new" && n.Node.ID == "t4" {
			t4 = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	if t4 == nil {
		t.Fatal("t4 missing from the new line")
	}
	found := false
	for _, c := range t4.Children {
		if c.SessionID == "br" {
			found = true
		}
	}
	if !found {
		t.Fatal("the branch did not re-attach to t4 of the new line")
	}
}

func TestABranchOffARemovedTurnBecomesAMarkedRoot(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{
		"br": {GraftedFrom: store.From{SessionID: "old", Node: "t2"}},
	}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut})
	roots := Build([]adapter.Session{sess("new", "t1", "t4"), sess("old", "t1", "t2", "t4"), sess("br", "b1")}, st)
	for _, r := range roots {
		if r.SessionID == "br" {
			if !r.FromRemoved {
				t.Fatal("the orphaned branch is not marked as coming from a removed stretch")
			}
			return
		}
	}
	t.Fatal("the orphaned branch is not a root")
}

func TestTheCutMarkerSitsWhereTheCutWas(t *testing.T) {
	st := &store.Store{Branches: map[string]store.Branch{}}
	st.Replace("old", "new", store.Branch{Kind: store.KindCut, Cut: &store.Cut{Turns: 2, At: "t4"}})
	st.Replace("old2", "new2", store.Branch{Kind: store.KindCut, Cut: &store.Cut{Turns: 3}})
	roots := Build([]adapter.Session{sess("new", "t1", "t4"), sess("new2", "u1", "u2")}, st)
	var here, after int
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.SessionID == "new" && n.Node.ID == "t4" {
			here = n.CutHere
		}
		if n.SessionID == "new2" && n.IsSessionLeaf {
			after = n.CutAfter
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	if here != 2 || after != 3 {
		t.Fatalf("CutHere %d on t4, CutAfter %d on the leaf; want 2 and 3", here, after)
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/tree/ -v 2>&1 | tail -20`
Expected: FAIL to compile, `FromRemoved` undefined.

- [ ] **Step 3: Implement in `internal/tree/tree.go`**

Add to `Node`, after `Grafted`:

```go
	FromRemoved bool // a branch whose turn was removed from the line it left
	CutHere     int  // turns cut immediately before this entry
	CutAfter    int  // turns cut after this entry, which ends its line
```

At the top of `Build`, before the sessions loop:

```go
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
```

In the sessions loop, first line: `if hidden[sess.ID] { continue }`. Inside the node loop after `n.Label = …`, add `if n.IsSessionLeaf { leafOf[sess.ID] = n }`.

In the edges loop, replace the lookup of `parentNodes` and `parent` with:

```go
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
```

After the edges loop, before building roots:

```go
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
```

- [ ] **Step 4: Run the tree tests**

Run: `go test ./internal/tree/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: `ok`.

- [ ] **Step 5: Write the failing render test**

Append to `internal/tui/view_test.go`:

```go
func TestRenderRowShowsCutsAndRemovedOrigins(t *testing.T) {
	here := &tree.Node{Node: adapter.Node{ID: "t4", Title: "four", Kind: adapter.KindHuman}, SessionID: "s", CutHere: 8}
	if got, _ := renderRow(Row{Node: here}, false, "", 120); !strings.Contains(got, "✂ 8 turns cut") {
		t.Fatalf("cut marker missing: %q", got)
	}
	end := &tree.Node{Node: adapter.Node{ID: "t2", Title: "two", Kind: adapter.KindHuman}, SessionID: "s", IsSessionLeaf: true, CutAfter: 3}
	if got, _ := renderRow(Row{Node: end}, false, "", 120); !strings.Contains(got, "✂ 3 turns cut after this") {
		t.Fatalf("trailing cut marker missing: %q", got)
	}
	orphan := &tree.Node{Node: adapter.Node{ID: "b1", Title: "b"}, SessionID: "br", IsSessionRoot: true, FromRemoved: true}
	if got, _ := renderRow(Row{Node: orphan}, false, "", 120); !strings.Contains(got, "from a removed stretch") {
		t.Fatalf("removed-origin marker missing: %q", got)
	}
}
```

- [ ] **Step 6: Run to see it fail**

Run: `go test ./internal/tui/ -run TestRenderRowShowsCuts -v`
Expected: FAIL.

- [ ] **Step 7: Implement in `renderRow`**

After the `if r.Node.IsSessionRoot { … shortID … }` block:

```go
	if r.Node.FromRemoved {
		b.WriteString("from a removed stretch  ")
	}
	if r.Node.CutHere > 0 {
		b.WriteString(fmt.Sprintf("✂ %d turns cut  ", r.Node.CutHere))
	}
```

After `b.WriteString(title)`:

```go
	if r.Node.CutAfter > 0 {
		b.WriteString(fmt.Sprintf("   ✂ %d turns cut after this", r.Node.CutAfter))
	}
```

- [ ] **Step 8: Run everything, then mutation check**

Run: `go test ./...` — expected: all `ok`.
Mutation: in `Build`, change `if br.ReplacedBy != "" && present[s.Resolve(id)]` to `if br.ReplacedBy != ""`. Run `go test ./internal/tree/ -run TestAReplacedLineShowsAgain`. Expected: FAIL. Revert.

- [ ] **Step 9: Commit**

```bash
go vet ./... && go test ./...
git add internal/tree/ internal/tui/view.go internal/tui/view_test.go
git commit -m "feat: hide replaced lines, re-attach their branches, and mark cuts

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Herdr — agent status, pane close, focused split

**Files:**
- Modify: `internal/herdr/herdr.go`, `internal/adapter/adapter.go` (`Resume` signature), `internal/claude/adapter.go`, `internal/tui/view.go` (three `Resume` callers), `internal/tui/view_test.go` (`fakeAdapter.Resume`)
- Test: `internal/herdr/herdr_test.go`

**Interfaces:**
- Produces:
  - `herdr.Split(cwd string, focus bool) (string, error)`
  - `herdr.AgentState(sessionID string) (pane, status string, err error)` — `"", "", nil` when no agent holds the session; an error when two panes claim it.
  - `herdr.ClosePane(paneID string) error`
  - `Adapter.Resume(sessionID, cwd string, focus bool) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/herdr/herdr_test.go`:

```go
func TestSplitFocusesOnlyWhenAsked(t *testing.T) {
	if got := strings.Join(splitArgv("/r", false), " "); !strings.HasSuffix(got, "--no-focus") {
		t.Fatalf("unfocused split argv %q", got)
	}
	if got := strings.Join(splitArgv("/r", true), " "); strings.Contains(got, "--no-focus") {
		t.Fatalf("focused split still passes --no-focus: %q", got)
	}
}

func TestAgentStateReportsPaneAndStatus(t *testing.T) {
	b, err := os.ReadFile("testdata/agent-list.json")
	if err != nil {
		t.Fatal(err)
	}
	stubHerdr(t, "cat <<'EOF'\n"+string(b)+"\nEOF")
	pane, status, err := AgentState("bdf6207b-7062-4f33-93c4-e86a0b3b438d")
	if err != nil || pane != "wA:p1" || status != "working" {
		t.Fatalf("got %q %q %v, want wA:p1 working", pane, status, err)
	}
	pane, status, err = AgentState("00000000-0000-4000-8000-000000000000")
	if err != nil || pane != "" || status != "" {
		t.Fatalf("an unheld session: got %q %q %v, want nothing", pane, status, err)
	}
}

// Two panes claiming one session is reachable (see parseAgentList). Closing
// either could kill the conversation the user is looking at, so it is an
// error, not a guess.
func TestAgentStateRefusesAnAmbiguousSession(t *testing.T) {
	stubHerdr(t, `echo '{"result":{"agents":[
{"pane_id":"p1","agent_status":"idle","agent_session":{"value":"s1"}},
{"pane_id":"p2","agent_status":"idle","agent_session":{"value":"s1"}}]}}'`)
	if _, _, err := AgentState("s1"); err == nil {
		t.Fatal("want an error for a session two panes claim")
	}
}

func TestClosePaneArgvAndRefusal(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	stubHerdr(t, `echo "$@" > `+log)
	if err := ClosePane("wA:p9"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	if strings.TrimSpace(string(got)) != "pane close wA:p9" {
		t.Fatalf("argv %q, want pane close wA:p9", got)
	}
	if err := ClosePane("-x"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("err = %v, want ErrUnsafeArgument", err)
	}
}
```

Add `"errors"`, `"path/filepath"` to the test imports if missing. Update the existing `splitArgv(...)` call in `TestArgvOrderingAndSeparator` to `splitArgv(..., false)`.

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/herdr/ -v 2>&1 | tail -20`
Expected: FAIL to compile.

- [ ] **Step 3: Implement in `internal/herdr/herdr.go`**

```go
func splitArgv(cwd string, focus bool) []string {
	argv := []string{"pane", "split", "--current", "--direction", "right", "--cwd", cwd}
	if !focus {
		argv = append(argv, "--no-focus")
	}
	return argv
}
```

Change `Split`:

```go
// Split opens a sibling pane to the right, taking focus only when asked. A
// branch opens beside the conversation the user is in and must not pull them
// away; a replacement is where they are going next.
func Split(cwd string, focus bool) (string, error) {
	if err := checkArg("cwd", cwd); err != nil {
		return "", err
	}
	out, err := run(splitArgv(cwd, focus)...)
	if err != nil {
		return "", err
	}
	return parseSplit(out)
}
```

Extend `agentListResp`'s agent struct with `AgentStatus string \`json:"agent_status"\``. Add:

```go
type agentInfo struct{ pane, status string }

// parseAgents is parseAgentList with each agent's status kept, and the
// sessions two panes claim reported rather than silently dropped.
func parseAgents(b []byte) (map[string]agentInfo, map[string]bool, error) {
	var r agentListResp
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, nil, err
	}
	out := map[string]agentInfo{}
	ambiguous := map[string]bool{}
	for _, a := range r.Result.Agents {
		if a.PaneID == "" || a.AgentSession == nil || a.AgentSession.Value == "" {
			continue // an agent still starting has no session yet
		}
		sid := a.AgentSession.Value
		if _, seen := out[sid]; seen {
			ambiguous[sid] = true
		}
		out[sid] = agentInfo{pane: a.PaneID, status: a.AgentStatus}
	}
	for sid := range ambiguous {
		delete(out, sid)
	}
	return out, ambiguous, nil
}
```

Rewrite `parseAgentList`'s body to call it (keep its doc comment):

```go
func parseAgentList(b []byte) (map[string]string, error) {
	agents, _, err := parseAgents(b)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(agents))
	for sid, a := range agents {
		out[sid] = a.pane
	}
	return out, nil
}
```

Add:

```go
// AgentState reports the pane holding sessionID and herdr's agent_status for
// it ("working", "idle", …). No agent holding it is not an error: both are
// empty. Two panes claiming it is, because the caller is about to close one.
func AgentState(sessionID string) (pane, status string, err error) {
	if err := checkArg("session id", sessionID); err != nil {
		return "", "", err
	}
	out, err := run(agentListArgv()...)
	if err != nil {
		return "", "", err
	}
	agents, ambiguous, err := parseAgents(out)
	if err != nil {
		return "", "", err
	}
	if ambiguous[sessionID] {
		return "", "", fmt.Errorf("%s is open in more than one pane", sessionID)
	}
	a := agents[sessionID]
	return a.pane, a.status, nil
}

// ClosePane closes a pane. herdr does not refuse a busy one; the caller
// checks agent_status first.
func ClosePane(paneID string) error {
	if err := checkArg("pane id", paneID); err != nil {
		return err
	}
	_, err := run("pane", "close", paneID)
	return err
}
```

- [ ] **Step 4: Thread `focus` through `Resume`**

`internal/adapter/adapter.go`: `Resume(sessionID, cwd string, focus bool) error`.
`internal/claude/adapter.go`: `func (claudeAdapter) Resume(sessionID, cwd string, focus bool) error` with `herdr.Split(cwd, focus)`.
`internal/tui/view.go`: the three existing calls `a.Resume(x, dst)` become `a.Resume(x, dst, false)` — branching, plain resume and fold-back keep today's unfocused behaviour.
`internal/tui/view_test.go`: `fakeAdapter` gains `focused bool` and

```go
func (f *fakeAdapter) Resume(sessionID, _ string, focus bool) error {
	f.resumed, f.focused = sessionID, focus
	return f.resumeErr
}
```

- [ ] **Step 5: Run everything**

Run: `go vet ./... && go test ./...`
Expected: all `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/herdr/ internal/adapter/ internal/claude/adapter.go internal/tui/
git commit -m "feat: herdr reports agent status per session, closes panes, splits with focus

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: The range menu, compact and cut, and the handover

**Files:**
- Create: `internal/tui/edit.go`, `internal/tui/edit_test.go`
- Modify: `internal/tui/view.go`, `cmd/herdr-tree/main.go`
- Test: `internal/tui/edit_test.go`; update existing tests in `internal/tui/view_test.go` listed in Step 9.

**Interfaces:**
- Consumes: `Adapter.Widen`, `Adapter.Splice`, `Adapter.Resume(…, focus)`, `Store.Replace`, `store.Kind*`, `store.Cut`, `herdr.AgentState`, `herdr.ClosePane`.
- Produces:
  - `type LiveFunc func(sessionID string) (pane, status string, err error)`
  - `type ClosePaneFunc func(paneID string) error`
  - `tui.Run(a, repoRoot, st, sessions, current, liveAgent, send, live, closePane)`
  - `editOp`, `editCmd`, `(u uiModel) editConfirm(kind string) (tea.Model, tea.Cmd)`, `u.menu`, `u.menuIdx`

- [ ] **Step 1: Write the failing tests**

`internal/tui/edit_test.go`:

```go
package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

// herdrLog records what the handover asked herdr for, in order.
type herdrLog struct {
	status   []string // successive answers to live(); the last repeats
	liveErr  error
	closeErr error
	calls    []string
}

func (h *herdrLog) live(sid string) (string, string, error) {
	h.calls = append(h.calls, "live "+sid)
	if h.liveErr != nil {
		return "", "", h.liveErr
	}
	if len(h.status) == 0 {
		return "", "", nil
	}
	s := h.status[0]
	if len(h.status) > 1 {
		h.status = h.status[1:]
	}
	return "pane-1", s, nil
}

func (h *herdrLog) close(p string) error {
	h.calls = append(h.calls, "close "+p)
	return h.closeErr
}

func rangeUI(t *testing.T, fa *fakeAdapter, h *herdrLog) uiModel {
	t.Helper()
	u := uiModel{m: New(session("s", "t1", "t2", "t3")), a: fa, st: loadedStore(t), repoRoot: "/repo",
		live: h.live, closePane: h.close}
	u.m.Cursor = 2
	next, _ := u.Update(key('s'))
	u = next.(uiModel)
	u.m.Cursor = 1
	return u
}

func press(t *testing.T, u uiModel, msgs ...tea.KeyMsg) (uiModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, m := range msgs {
		var next tea.Model
		next, cmd = u.Update(m)
		u = next.(uiModel)
	}
	return u, cmd
}

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	down  = tea.KeyMsg{Type: tea.KeyDown}
	esc   = tea.KeyMsg{Type: tea.KeyEsc}
)

func TestARangeOpensAMenuNotACall(t *testing.T) {
	fa := &fakeAdapter{}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{}), enter)
	if cmd != nil || u.menu != "range" {
		t.Fatalf("want the range menu and no command; menu=%q", u.menu)
	}
	for _, want := range []string{"summarise & compact", "cut"} {
		if !strings.Contains(u.View(), want) {
			t.Fatalf("menu does not offer %q:\n%s", want, u.View())
		}
	}
	u, _ = press(t, u, esc)
	if u.menu != "" || u.m.RangeEnd == nil {
		t.Fatal("esc on the menu must close it and keep the range")
	}
}

func TestCutConfirmsOnceThenSplicesAndReplaces(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{}), enter, down, enter)
	if cmd != nil {
		t.Fatal("cut ran before its confirmation")
	}
	for _, want := range []string{"turns 2–3", "Costs nothing", "hidden"} {
		if !strings.Contains(u.confirm, want) {
			t.Fatalf("cut confirmation lacks %q:\n%s", want, u.confirm)
		}
	}
	u, cmd = press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 1 || fa.spliced[0].Seed != "" {
		t.Fatalf("want one cut, got %+v", fa.spliced)
	}
	if fa.resumed != "" {
		t.Fatal("a session no pane holds must not be opened")
	}
	if u.st.Branches["s"].ReplacedBy != "spliced-sid" {
		t.Fatal("the old line was not marked replaced")
	}
	nb := u.st.Branches["spliced-sid"]
	if nb.Kind != store.KindCut || nb.Cut == nil || nb.Cut.Turns != 2 || nb.Cut.At != "t3" {
		t.Fatalf("new record %+v", nb)
	}
	if !msg.reload || msg.quit {
		t.Fatalf("a not-live edit reloads the tree and stays open: %+v", msg)
	}
}

func TestCompactSummarisesThenSplicesWithTheSeed(t *testing.T) {
	fa := &fakeAdapter{summary: "it went well", span: adapter.Span{First: 2, Last: 3}}
	u, _ := press(t, rangeUI(t, fa, &herdrLog{}), enter, enter)
	if !strings.Contains(u.confirm, "billed") || !strings.Contains(u.confirm, "replaced by the summary") {
		t.Fatalf("compact confirmation:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	cmd()
	if fa.summarisedFrom != "t2" || fa.summarisedTo != "t3" {
		t.Fatalf("summarised %q..%q", fa.summarisedFrom, fa.summarisedTo)
	}
	if len(fa.spliced) != 1 || !strings.HasPrefix(fa.spliced[0].Seed, claudeCompactionPrefix) ||
		!strings.Contains(fa.spliced[0].Seed, "it went well") {
		t.Fatalf("splice seed %+v", fa.spliced)
	}
	if len(u.st.AllSummaries()) != 1 {
		t.Fatal("the summary must still be stored for p")
	}
}

func TestAFailedSummaryWritesAndClosesNothing(t *testing.T) {
	fa := &fakeAdapter{summariseErr: errors.New("claude: limit reached"), span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 0 || fa.resumed != "" {
		t.Fatal("something was written or opened after the summary failed")
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "close") {
			t.Fatal("a pane was closed after the summary failed")
		}
	}
	if msg.quit {
		t.Fatal("a failure must stay on screen")
	}
}

func TestAWorkingAgentIsRefusedAtConfirm(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{status: []string{"working"}}), enter, down, enter)
	if cmd != nil || u.confirm != "" {
		t.Fatal("a working agent's session reached the confirmation")
	}
	if !strings.Contains(u.status, "working") || u.m.RangeEnd == nil {
		t.Fatalf("status %q; the range must survive a refusal", u.status)
	}
}

// The summary call takes minutes; the user may start a turn in the old pane
// meanwhile. The summary is kept, nothing is spliced.
func TestAnAgentThatStartsWorkingDuringTheSummaryStopsTheSplice(t *testing.T) {
	fa := &fakeAdapter{summary: "s", span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle", "working"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if len(fa.spliced) != 0 {
		t.Fatal("spliced while the agent was working")
	}
	if !strings.Contains(msg.status, "summary stored") {
		t.Fatalf("status %q", msg.status)
	}
	if len(u.st.AllSummaries()) != 1 {
		t.Fatal("the paid-for summary was lost")
	}
}

func TestALiveEditOpensTheNewLineFocusedThenClosesTheOld(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, down, enter)
	if !strings.Contains(u.confirm, "Text typed but not sent") {
		t.Fatalf("a live confirmation must warn about the pane closing:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if fa.resumed != "spliced-sid" || !fa.focused {
		t.Fatalf("resumed %q focused=%v, want the new line with focus", fa.resumed, fa.focused)
	}
	if h.calls[len(h.calls)-1] != "close pane-1" {
		t.Fatalf("herdr calls %v, want the old pane closed last", h.calls)
	}
	if !msg.quit {
		t.Fatalf("a completed handover leaves: %q", msg.status)
	}
}

func TestIfTheNewLineDoesNotOpenTheOldPaneStays(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}, resumeErr: errors.New("split refused")}
	h := &herdrLog{status: []string{"idle"}}
	u, _ := press(t, rangeUI(t, fa, h), enter, down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	for _, c := range h.calls {
		if strings.HasPrefix(c, "close") {
			t.Fatal("the old pane was closed though the new one never opened")
		}
	}
	if !strings.Contains(msg.status, "old pane left running") || msg.quit {
		t.Fatalf("status %q quit=%v", msg.status, msg.quit)
	}
}

func TestAFailedCloseIsReportedNotUndone(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	h := &herdrLog{status: []string{"idle"}, closeErr: errors.New("no such pane")}
	u, _ := press(t, rangeUI(t, fa, h), enter, down, enter)
	_, cmd := press(t, u, enter)
	msg := cmd().(actionDoneMsg)
	if !strings.Contains(msg.status, "old pane did not close") || msg.quit {
		t.Fatalf("status %q quit=%v", msg.status, msg.quit)
	}
	if u.st.Branches["s"].ReplacedBy == "" {
		t.Fatal("a failed close must not undo the replacement")
	}
}

func TestHerdrNotAnsweringRefusesTheEdit(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 2, Last: 3}}
	u, cmd := press(t, rangeUI(t, fa, &herdrLog{liveErr: errors.New("herdr agent list timed out")}), enter, down, enter)
	if cmd != nil || u.confirm != "" {
		t.Fatal("an edit went ahead without knowing whether a pane holds the session")
	}
}

func TestNoEditStatusCarriesTheSeed(t *testing.T) {
	fa := &fakeAdapter{summary: sentinel, span: adapter.Span{First: 2, Last: 3},
		spliceErr: errors.New("write failed: " + sentinel)}
	u, _ := press(t, rangeUI(t, fa, &herdrLog{}), enter, enter)
	_, cmd := press(t, u, enter)
	if msg := cmd().(actionDoneMsg); strings.Contains(msg.status, sentinel) {
		t.Fatalf("status leaked the summary: %q", msg.status)
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/tui/ -run 'TestARange|TestCut|TestCompact|TestAFailed|TestAWorking|TestAnAgent|TestALive|TestIfTheNew|TestHerdr|TestNoEdit' -v 2>&1 | tail -20`
Expected: FAIL to compile (`u.menu`, `live`, `closePane` undefined).

- [ ] **Step 3: Write `internal/tui/edit.go`**

```go
package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

// LiveFunc reports the pane holding a session and herdr's agent_status for
// it; both empty when no pane holds it. ClosePaneFunc closes a pane. Both are
// injected, like SendFunc, so the handover is testable without Herdr.
type (
	LiveFunc      func(sessionID string) (pane, status string, err error)
	ClosePaneFunc func(paneID string) error
)

// editOp is one context edit, captured when its confirmation is raised.
type editOp struct {
	src       adapter.Session
	edit      adapter.Edit
	kind      string     // store.KindCompacted | KindCut | KindInserted
	summarise bool       // compact: the seed is produced first
	from, to  *tree.Node // compact: the range to summarise
	title     string     // row title for the new record
	pane      string     // pane holding src at confirm time; "" if none
	dst       string
}

// editCmd runs an edit to completion or to its first failure, in the order
// the spec fixes (§6.1). Each step runs only if the one before succeeded, and
// every status says what DID happen.
func editCmd(a adapter.Adapter, st *store.Store, op editOp, live LiveFunc, closePane ClosePaneFunc) tea.Cmd {
	return func() tea.Msg {
		if op.summarise {
			text, err := a.Summarise(op.src, op.from.Node.ID, op.to.Node.ID)
			if err != nil {
				return actionDoneMsg{status: "summarise failed: " + err.Error() + " — nothing was written"}
			}
			sum := store.Summary{Text: text, SessionID: op.src.ID,
				FromTurn: op.from.Node.ID, ToTurn: op.to.Node.ID, CreatedAt: time.Now().UTC()}
			st.AddSummary(sum)
			if err := st.Save(); err != nil {
				return actionDoneMsg{status: "summarised, but not saved: " + err.Error() + " — nothing was written"}
			}
			op.edit.Seed = foldBackSeed(op.from, sum, true)
			op.title = "⤶ " + title(text, 40)
		}
		if op.pane != "" {
			// The summary call takes minutes. Whatever was true at confirm
			// time is checked again right before the transcript is read.
			pane, status, err := live(op.src.ID)
			if err != nil {
				return actionDoneMsg{status: "cannot tell whether the session is busy: " + scrubbed(err, op.edit.Seed) + " — nothing was written"}
			}
			if status == "working" {
				if op.summarise {
					return actionDoneMsg{status: "summary stored — agent is busy; select again or use p"}
				}
				return actionDoneMsg{status: "agent is working — wait for it to finish; nothing was written"}
			}
			op.pane = pane
		}
		res, err := a.Splice(op.src, op.edit, op.dst)
		if err != nil {
			return actionDoneMsg{status: op.kind + " failed: " + scrubbed(err, op.edit.Seed)}
		}
		b := store.Branch{Kind: op.kind, Title: op.title, CreatedAt: time.Now().UTC()}
		if op.kind == store.KindCut {
			b.Cut = &store.Cut{Turns: res.Removed, At: res.After}
		}
		st.Replace(op.src.ID, res.SessionID, b)
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + ", but the tree was not saved: " + err.Error()}
		}
		if op.pane == "" {
			return actionDoneMsg{status: op.kind + " " + shortID(op.src.ID) + " → " + shortID(res.SessionID), reload: true,
				from: op.src.ID, to: res.SessionID}
		}
		if err := a.Resume(res.SessionID, op.dst, true); err != nil {
			return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + ", but it did not open — old pane left running: " + err.Error(),
				reload: true, from: op.src.ID, to: res.SessionID}
		}
		if err := closePane(op.pane); err != nil {
			return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + " and opened, but the old pane did not close: " + err.Error()}
		}
		return actionDoneMsg{status: op.kind + " into " + shortID(res.SessionID) + " — opened in a new pane, old pane closed", quit: true}
	}
}

const replacesLine = "A new session replaces this line in the tree (the old one is hidden, kept on disk)."
const liveLine = "The pane running this session is closed and the new one opens. Text typed but not sent in that pane is lost."

// editConfirm raises the one confirmation for a range edit (§2.3). kind is
// store.KindCompacted or store.KindCut.
func (u uiModel) editConfirm(kind string) (tea.Model, tea.Cmd) {
	from, to, ok := u.m.RangeSpan()
	if !ok {
		u.m.CancelRange()
		u.status = "the range is no longer on screen"
		return u, nil
	}
	if from.SessionID != to.SessionID {
		u.status = "a range must stay inside one session"
		return u, nil
	}
	src := adapter.Session{ID: to.SessionID, CWD: to.SessionCWD, Path: to.SessionPath}
	pane, ok := u.liveCheck(src.ID)
	if !ok {
		return u, nil
	}
	sp, err := u.a.Widen(src, from.Node.ID, to.Node.ID)
	if err != nil {
		u.status = "cannot edit this range: " + err.Error()
		return u, nil
	}
	op := editOp{src: src, edit: adapter.Edit{From: from.Node.ID, To: to.Node.ID}, kind: kind,
		from: from, to: to, pane: pane, dst: u.dstCWD(to), title: "✂ " + from.Node.Title}
	var text string
	if kind == store.KindCompacted {
		turns, entries, size, err := u.a.Preview(src, sp.End)
		if err != nil {
			u.status = "cannot summarise this range: " + err.Error()
			return u, nil
		}
		op.summarise = true
		text = fmt.Sprintf("Summarise & compact turns %d–%d:\n\n  from  %q\n  to    %q\n\nThe model reads this session up to the end of the range — %d turn(s) · %d entries · %s — and describes only the range. That whole prefix is billed.\n\nThen: turns %d–%d are replaced by the summary.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, turns, entries, humanBytes(size), sp.First, sp.Last, replacesLine)
		u.pendingBusy = "summarising…"
	} else {
		text = fmt.Sprintf("Cut turns %d–%d:\n\n  from  %q\n  to    %q\n\nRemoves turns %d–%d. Costs nothing. No note is left in the conversation.\n%s",
			sp.First, sp.Last, from.Node.Title, to.Node.Title, sp.First, sp.Last, replacesLine)
		u.pendingBusy = "cutting…"
	}
	if pane != "" {
		text += "\n" + liveLine
	}
	u.confirm = text + "\n\n[enter] go   [esc] back"
	u.pending = editCmd(u.a, u.st, op, u.live, u.closePane)
	return u, nil
}

// liveCheck resolves which pane holds sessionID. ok=false means the edit is
// refused and u.status says why: a working agent, or a herdr that will not
// say — an edit that might close a pane must know, not guess.
func (u *uiModel) liveCheck(sessionID string) (pane string, ok bool) {
	if u.live == nil {
		return "", true
	}
	pane, status, err := u.live(sessionID)
	if err != nil {
		u.status = "cannot tell whether this session is open: " + err.Error()
		return "", false
	}
	if status == "working" {
		u.status = "agent is working — wait for it to finish"
		return "", false
	}
	return pane, true
}

var rangeMenu = []string{"summarise & compact", "cut"}

func menuView(heading string, options []string, idx int) string {
	s := heading + "\n\n"
	for i, o := range options {
		marker := "  "
		if i == idx {
			marker = "> "
		}
		s += marker + o + "\n"
	}
	return s + "\n↑↓ choose   [enter] select   [esc] back\n"
}
```

Note the `u.pendingBusy` assignment happens on the value receiver `u` and is returned with it, which is how the rest of `view.go` already works.

- [ ] **Step 4: Wire it into `view.go`**

In `uiModel`, add after `liveAgent`:

```go
	live      LiveFunc
	closePane ClosePaneFunc

	// menu is "range" or "place" while one of the two menus is open.
	menu    string
	menuIdx int
```

Extend `actionDoneMsg`:

```go
type actionDoneMsg struct {
	status string
	quit   bool
	// reload re-reads the sessions: an edit that opened nothing left the
	// overlay up, and the tree on screen still shows the old line. from/to
	// name the replacement, so the scope can follow it.
	reload   bool
	from, to string
}
```

In `Update`'s `case actionDoneMsg:` after `u.status = msg.status`:

```go
		if msg.reload {
			if sessions, err := u.a.Discover(u.repoRoot); err == nil {
				if u.current == msg.from && u.liveAgent == "" {
					u.current = msg.to
				}
				u.roots = tree.Build(sessions, u.st)
				u.m.RangeEnd = nil
				u.rebuild()
			}
		}
```

The `u.liveAgent == ""` guard matters: `agentFor` sends to `liveAgent` for any node of `u.current`, and the live agent is on the OLD session. Never point `current` at a session that agent is not running.

Before the `if u.confirm != ""` block, add the menu handler:

```go
		if u.menu != "" {
			options := rangeMenu
			if u.menu == "place" {
				options = placeMenu
			}
			switch msg.String() {
			case "up", "k":
				if u.menuIdx > 0 {
					u.menuIdx--
				}
			case "down", "j":
				if u.menuIdx < len(options)-1 {
					u.menuIdx++
				}
			case "enter":
				which, idx := u.menu, u.menuIdx
				u.menu, u.menuIdx = "", 0
				if which == "range" {
					if idx == 0 {
						return u.editConfirm(store.KindCompacted)
					}
					return u.editConfirm(store.KindCut)
				}
				return u.placeChosen(idx)
			case "esc", "q":
				u.menu, u.menuIdx = "", 0
			}
			return u, nil
		}
```

(`placeMenu` and `placeChosen` arrive in Task 7. For this task add, in `edit.go`, `var placeMenu = []string{"insert here", "branch here"}` and a stub `func (u uiModel) placeChosen(int) (tea.Model, tea.Cmd) { return u, nil }` so the package compiles; Task 7 replaces the stub.)

Replace both `return u.summariseConfirm()` sites (under `case "s":` and `case "enter":`) with `return u.openRangeMenu()`, and add to `edit.go`:

```go
func (u uiModel) openRangeMenu() (tea.Model, tea.Cmd) {
	from, to, ok := u.m.RangeSpan()
	if !ok {
		u.m.CancelRange()
		u.status = "the range is no longer on screen"
		return u, nil
	}
	if from.SessionID != to.SessionID {
		u.status = "a range must stay inside one session"
		return u, nil
	}
	u.menu, u.menuIdx = "range", 0
	return u, nil
}
```

In `View`, after the `if u.picking != nil` block:

```go
	if u.menu == "range" {
		return menuView("Do what with this range?", rangeMenu, u.menuIdx)
	}
	if u.menu == "place" {
		return menuView(fmt.Sprintf("Fold the summary in at:  %q", u.pickAt.Node.Title), placeMenu, u.menuIdx)
	}
```

Footer: `s summarise` → `s select`; the in-range footer `s/⏎ summarise` → `s/⏎ choose what to do`.

Delete `summariseConfirm`, `summariseConfirmText` and `summariseCmd` from `view.go` — `editConfirm`/`editCmd` replace them.

Extend `Run`:

```go
func Run(a adapter.Adapter, repoRoot string, st *store.Store, sessions []adapter.Session, current, liveAgent string, send SendFunc, live LiveFunc, closePane ClosePaneFunc) error {
	roots := tree.Build(sessions, st)
	u := uiModel{m: New(roots), a: a, st: st, repoRoot: repoRoot, current: current, roots: roots,
		liveAgent: liveAgent, send: send, live: live, closePane: closePane}
```

`cmd/herdr-tree/main.go`: `return tui.Run(a, root, st, sessions, current, liveAgent, herdr.AgentPrompt, herdr.AgentState, herdr.ClosePane)`.

- [ ] **Step 5: Run the new tests**

Run: `go test ./internal/tui/ -run 'TestARange|TestCut|TestCompact|TestAFailed|TestAWorking|TestAnAgent|TestALive|TestIfTheNew|TestHerdr|TestNoEdit' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: all PASS.

- [ ] **Step 6: Update the existing tests the menu changes**

Run: `go test ./internal/tui/ 2>&1 | grep -- '--- FAIL'`. The expected failures, and the fix for each:

- `TestSKeyFixesTheRangeEndThenConfirmsBeforeSummarising` — after the second `s`, press `enter` (menu → compact) before asserting on `confirm`. The confirmation now names both ends and says `turns`; replace the `"3 row(s)"` assertion with the widened turn numbers from `fakeAdapter.span` (set `span: adapter.Span{First: 1, Last: 3}` on its `fakeAdapter`). After confirming, `cmd3()` runs `editCmd`: assert `fa.summarisedFrom == "t1"` and `len(fa.spliced) == 1`.
- `TestSummariseStoresTheRangeAndKeepsTheOverlayOpen`, `TestFailedSummariseStoresNothingAndKeepsTheOverlayOpen` — they call the deleted `summariseCmd`. Rewrite each to build `editOp{src: adapter.Session{ID: "s"}, kind: store.KindCompacted, summarise: true, from: from, to: to}` and call `editCmd(fa, st, op, nil, nil)()`; keep their assertions about the stored summary; for the success case also assert `msg.reload`.
- `TestARangeMayNotSpanTwoSessions`, `TestEscapingTheCostDialogKeepsTheRange`, `TestNoStatusLineCarriesTheSummary` — insert the `enter` that picks from the menu where the test previously expected the confirmation directly. Their assertions stand.

Every one of these tests keeps asserting what it asserted before; only the keystrokes that reach that state change. If a failure is not in this list, stop and report it — do not adjust its assertion.

- [ ] **Step 7: Mutation checks**

1. In `editCmd`, change `if status == "working"` (the re-check) to `if status == "busy"`. Run `go test ./internal/tui/ -run TestAnAgentThatStartsWorking`. Expected: FAIL. Revert.
2. Move `closePane(op.pane)` above `a.Resume(…)` (close before open). Run `go test ./internal/tui/ -run 'TestIfTheNewLine|TestALiveEdit'`. Expected: FAIL. Revert.

- [ ] **Step 8: Commit**

```bash
go vet ./... && go test ./...
git add internal/tui/ cmd/herdr-tree/main.go
git commit -m "feat: s selects, ⏎ offers compact or cut, and a live pane is handed over

Mutations checked: a re-check that never matches, and closing the old
pane before the new one opens; both turn the suite red.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: `p` offers insert here or branch here

**Files:**
- Modify: `internal/tui/edit.go` (replace the `placeChosen` stub), `internal/tui/view.go` (picker enter, picker text)
- Test: `internal/tui/edit_test.go`; update existing picker tests in `view_test.go`

**Interfaces:**
- Consumes: Task 6's `editOp`, `editCmd`, `liveCheck`, `menu`, `placeMenu`.
- Produces: `(u uiModel) placeChosen(idx int) (tea.Model, tea.Cmd)`; `u.placing store.Summary` holding the summary chosen in the picker.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/edit_test.go`:

```go
func pickUI(t *testing.T, fa *fakeAdapter, h *herdrLog) uiModel {
	t.Helper()
	st := loadedStore(t)
	st.AddSummary(store.Summary{Text: "what the branch found", SessionID: "other", FromTurn: "a", ToTurn: "b"})
	u := uiModel{m: New(session("s", "t1", "t2", "t3")), a: fa, st: st, repoRoot: "/repo",
		live: h.live, closePane: h.close}
	u.m.Cursor = 0
	u, _ = press(t, u, key('p'), enter)
	return u
}

func TestPickingASummaryAwayFromTheLiveTipOffersInsertOrBranch(t *testing.T) {
	u := pickUI(t, &fakeAdapter{}, &herdrLog{})
	if u.menu != "place" {
		t.Fatalf("menu %q, want place", u.menu)
	}
	for _, want := range []string{"insert here", "branch here"} {
		if !strings.Contains(u.View(), want) {
			t.Fatalf("placement menu lacks %q:\n%s", want, u.View())
		}
	}
}

func TestInsertSplicesTheSummaryInAndKeepsWhatFollows(t *testing.T) {
	fa := &fakeAdapter{span: adapter.Span{First: 1, Last: 1}}
	u, cmd := press(t, pickUI(t, fa, &herdrLog{}), enter)
	if cmd != nil || !strings.Contains(u.confirm, "Everything after it is kept") {
		t.Fatalf("insert must confirm first:\n%s", u.confirm)
	}
	_, cmd = press(t, u, enter)
	cmd()
	if len(fa.spliced) != 1 {
		t.Fatalf("spliced %+v", fa.spliced)
	}
	e := fa.spliced[0]
	if e.After != "t1" || e.From != "" || !strings.HasPrefix(e.Seed, claudeSummaryPrefix) {
		t.Fatalf("edit %+v, want an insert after t1 seeded with the summary", e)
	}
	if u.st.Branches["spliced-sid"].Kind != store.KindInserted {
		t.Fatal("the new record is not marked inserted")
	}
}

func TestBranchHereIsTodaysFoldBack(t *testing.T) {
	fa := &fakeAdapter{}
	u, _ := press(t, pickUI(t, fa, &herdrLog{}), down, enter)
	if !strings.Contains(u.confirm, "NEW session") {
		t.Fatalf("branch here should raise the v2 fold-back confirmation:\n%s", u.confirm)
	}
	_, cmd := press(t, u, enter)
	cmd()
	if fa.seededWith == "" || len(fa.spliced) != 0 {
		t.Fatal("branch here must graft, not splice")
	}
	if fa.focused {
		t.Fatal("a branch opens beside the user, unfocused")
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/tui/ -run 'TestPickingASummary|TestInsertSplices|TestBranchHere' -v 2>&1 | tail -15`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `uiModel` add `placing store.Summary // the summary chosen in the picker, while the placement menu is open`.

In `Update`'s picker `case "enter":`, keep the live-tip send branch exactly as it is. Replace everything after it (the `Preview`/`foldBackConfirmText` part) with:

```go
				u.placing, u.pickAt = sum, at
				u.menu, u.menuIdx = "place", 0
				return u, nil
```

and remove `u.pickAt` from the line that clears the picker at the top of that case (the placement menu still needs it); clear it in `placeChosen` and on `esc` from the menu instead (add `u.pickAt = nil` to the menu's `esc` branch).

Replace the `placeChosen` stub in `edit.go`:

```go
// placeChosen acts on the placement menu. Insert rewrites the line in place
// and hides the old one; branch is v2's seeded graft and leaves both visible.
func (u uiModel) placeChosen(idx int) (tea.Model, tea.Cmd) {
	at, sum := u.pickAt, u.placing
	u.pickAt = nil
	src := adapter.Session{ID: at.SessionID, CWD: at.SessionCWD, Path: at.SessionPath}
	if idx == 1 {
		turns, entries, size, err := u.a.Preview(src, at.Node.ID)
		if err != nil {
			u.status = "cannot fold back here: " + err.Error()
			return u, nil
		}
		u.confirm = foldBackConfirmText(at, turns, entries, size, u.dstCWD(at))
		u.pending = foldBackCmd(u.a, u.st, at, u.dstCWD(at), sum, u.agentFor(at), u.send)
		u.pendingBusy = "folding back…"
		return u, nil
	}
	pane, ok := u.liveCheck(at.SessionID)
	if !ok {
		return u, nil
	}
	sp, err := u.a.Widen(src, at.Node.ID, at.Node.ID)
	if err != nil {
		u.status = "cannot insert here: " + err.Error()
		return u, nil
	}
	// Nothing is removed, so nothing contracted: an insert is always marked as
	// knowledge arriving, whatever session the summary came from.
	seed := foldBackSeed(at, sum, false)
	op := editOp{src: src, edit: adapter.Edit{After: at.Node.ID, Seed: seed}, kind: store.KindInserted,
		pane: pane, dst: u.dstCWD(at), title: "⤶ " + title(sum.Text, 40)}
	text := fmt.Sprintf("Insert the summary after turn %d:  %q\n\nEverything after it is kept. Costs nothing.\n%s", sp.Last, at.Node.Title, replacesLine)
	if pane != "" {
		text += "\n" + liveLine
	}
	u.confirm = text + "\n\n[enter] insert   [esc] back"
	u.pending, u.pendingBusy = editCmd(u.a, u.st, op, u.live, u.closePane), "inserting…"
	return u, nil
}
```

In `pickerView`, replace the `else` text `"Starts a NEW session that rewinds to this turn and carries the summary.\n"` with `"Next: insert it here, or branch here.\n"`.

- [ ] **Step 4: Run the new tests**

Run: `go test ./internal/tui/ -run 'TestPickingASummary|TestInsertSplices|TestBranchHere' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: PASS.

- [ ] **Step 5: Update the existing picker tests**

Run: `go test ./internal/tui/ 2>&1 | grep -- '--- FAIL'`. Expected failures and fixes:

- `TestPickerFoldsTheChosenSummaryInAtTheSelectedTurn` — its first `View()` assertion looked for `"NEW session"` in the picker; change that to `"insert it here, or branch here"`. After the picker's `enter`, press `down`, `enter` (branch here) before the fold-back confirmation assertions. The rest stands.
- `TestOnlyTheLiveSessionsTipIsSentTo`, `TestThePickerOnlyPromisesToSendAtTheTip` — where they press `enter` to reach the graft confirmation, insert `down`, `enter` first. Their assertions (nothing sent to another session's agent) stand.

As in Task 6: only keystrokes change. Any other failure — stop and report.

- [ ] **Step 6: Commit**

```bash
go vet ./... && go test ./...
git add internal/tui/
git commit -m "feat: p offers insert here, keeping what follows, beside branch here

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Check every splice against the real transcripts

**Files:**
- Create: `cmd/splicecheck/main.go`

**Interfaces:**
- Consumes: `claude.Splice`, `claude.ProjectsDir`, `claude.ParseFile`, `claude.Entry`.

This spends nothing. It reads `~/.claude/projects/*/*.jsonl`, splices into a temp directory (`CLAUDE_PROJECTS_DIR` pointed at it), validates each result, and deletes the temp directory. It prints counts only — never text, titles or paths of the user's sessions beyond a short session id.

- [ ] **Step 1: Write the check**

`cmd/splicecheck/main.go`:

```go
// Command splicecheck splices every local transcript at many points into a
// temp directory and checks that each result is a transcript Claude Code
// could have written: every tool_use answered, every parent present, one
// root. Read-only on the user's files; prints counts, never content.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/claude"
)

func main() {
	src := claude.ProjectsDir()
	paths, _ := filepath.Glob(filepath.Join(src, "*", "*.jsonl"))
	tmp, err := os.MkdirTemp("", "splicecheck-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)
	os.Setenv("CLAUDE_PROJECTS_DIR", tmp) // Splice writes here, never into src

	var tried, ok, refused, bad int
	for _, p := range paths {
		es, skipped, err := claude.ParseFile(p)
		if err != nil || skipped > 0 {
			continue
		}
		var prompts []string
		for _, e := range es {
			if claude.IsPrompt(e) {
				prompts = append(prompts, e.UUID())
			}
		}
		// ponytail: windows of one to three turns, plus one insert per turn,
		// not every range. O(turns) splices per session keeps it to seconds;
		// widen the window if a failure ever hides in longer ranges.
		for i := range prompts {
			for w := 0; w < 3 && i+w < len(prompts); w++ {
				for _, e := range []adapter.Edit{
					{From: prompts[i], To: prompts[i+w]},
					{From: prompts[i], To: prompts[i+w], Seed: claude.CompactionPrefix + " check"},
				} {
					tried++
					switch r := check(p, e); r {
					case "":
						ok++
					case "refused":
						refused++
					default:
						bad++
						fmt.Printf("BAD %s turn %d+%d seed=%v: %s\n", shortID(p), i+1, w, e.Seed != "", r)
					}
				}
			}
			tried++
			switch r := check(p, adapter.Edit{After: prompts[i], Seed: claude.SummaryPrefix + " check"}); r {
			case "":
				ok++
			case "refused":
				refused++
			default:
				bad++
				fmt.Printf("BAD %s insert after turn %d: %s\n", shortID(p), i+1, r)
			}
		}
	}
	fmt.Printf("sessions %d · splices %d · valid %d · refused %d · BAD %d\n", len(paths), tried, ok, refused, bad)
	if bad > 0 {
		os.Exit(1)
	}
}

func shortID(p string) string {
	b := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// check splices once and validates the result. "" is valid; "refused" is a
// splice the code declined; anything else names the defect.
func check(path string, e adapter.Edit) string {
	res, err := claude.Splice(path, e, "/splicecheck")
	if err != nil {
		return "refused"
	}
	out, _ := filepath.Glob(filepath.Join(claude.ProjectsDir(), "*", res.SessionID+".jsonl"))
	if len(out) != 1 {
		return "no output file"
	}
	defer os.Remove(out[0])
	es, skipped, err := claude.ParseFile(out[0])
	if err != nil || skipped > 0 {
		return "output does not parse"
	}
	have := map[string]bool{}
	uses, results := map[string]bool{}, map[string]bool{}
	for _, en := range es {
		if u := en.UUID(); u != "" {
			have[u] = true
		}
		msg, _ := en.Raw["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			switch bm["type"] {
			case "tool_use":
				id, _ := bm["id"].(string)
				uses[id] = true
			case "tool_result":
				id, _ := bm["tool_use_id"].(string)
				results[id] = true
			}
		}
	}
	roots := 0
	for _, en := range es {
		if en.UUID() == "" {
			continue
		}
		p := en.ParentUUID()
		if p == "" {
			roots++
		} else if !have[p] {
			return "an entry's parent is missing"
		}
	}
	if roots != 1 {
		return fmt.Sprintf("%d roots", roots)
	}
	for id := range uses {
		if !results[id] {
			return "a tool_use has no tool_result"
		}
	}
	for id := range results {
		if !uses[id] {
			return "a tool_result has no tool_use"
		}
	}
	return ""
}
```

`claude.IsPrompt` already exists and is exported. `ProjectsDir` re-reads `CLAUDE_PROJECTS_DIR` on every call, so `src` must be captured before `os.Setenv` — as written. Never point `src` at the temp dir.

- [ ] **Step 2: Build and run it**

It reads only local files and spends nothing, so no approval is needed; still, say you are running it.

Run: `go run ./cmd/splicecheck`
Expected: a final line `… BAD 0`. Paste the summary line into the task report.

If any `BAD` line appears: stop. Report the defect names and counts to the user. Do not "fix" the check to pass.

- [ ] **Step 3: Commit**

```bash
go vet ./... && go test ./...
git add cmd/splicecheck/
git commit -m "test: splice every local transcript at many points and validate the results

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: The one real resume — needs the user

This is spec §8's "not automated" item and §10 Q1–Q2. It runs `claude`, which spends budget. **Do not run it. Ask the user.** Present exactly this, then stop:

> The last unverified step is whether Claude Code resumes a spliced file. I'd do it once, by hand, in a throwaway directory:
> 1. `go build -o bin/herdr-tree.exe ./cmd/herdr-tree` (your live Herdr picks this binary up).
> 2. In a scratch repo, start a short Claude session (3–4 turns), open `prefix+t`, select turns 2–3, cut.
> 3. Confirm the new pane opens focused, the old one closes, and the new session answers a question about turn 4 without mentioning turns 2–3.
> 4. Repeat with summarise & compact (one summary call is billed).
>
> Want to run it yourself, or have me walk through it with you?

Record the outcome in `.superpowers/sdd/2026-09-23-context-editing/progress.md` (gitignored). This task has no commit unless the run finds a defect.

---

## Self-review against the spec

| Spec | Task |
|---|---|
| §2.1 `s` selects, footer | 6 |
| §2.2 range menu | 6 |
| §2.3 one confirmation, widened turns, live warning | 6 (compact, cut), 7 (insert) |
| §2.4 refusals: cross-session, working, whole cut, off-line | 6, 2 |
| §2.5 `p` insert/branch, live tip unchanged | 7 |
| §2.6 summary still stored | 6 (`editCmd`) |
| §3.1 whole turns, preamble | 1 |
| §3.2 what is written, uuids kept, fresh parse | 2 |
| §3.3 refusals | 2 |
| §3.4 native `/compact` | 1 (follows `parentUuid`, as `Select` does) |
| §4 compact end to end | 6 |
| §5.1 store fields, one-way merge, hide only if replacement present | 3, 4 |
| §5.2–5.4 hiding, re-attach, markers | 4 |
| §6.1 order, busy re-check, focus, close last | 5, 6 |
| §6.2 not live: nothing opened | 6 (`reload`) |
| §6.3 status lines, scrubbed | 6 |
| §8 unit tests, stubs, mutations, real-transcript check, manual run | 1–8, 9 |
