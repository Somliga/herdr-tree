# herdr-tree v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Herdr plugin that draws a repo-wide tree of Claude Code conversation turns and starts a new Claude session continuing from any turn in it.

**Architecture:** A core that knows nothing about Claude, behind a four-method `Adapter` interface. The Claude adapter reads `~/.claude/projects/*/*.jsonl` to build turn chains, and "branches" by writing a new transcript containing only the ancestor chain of a chosen turn — verified to be resumable by Claude Code. Herdr owns all process and pane lifecycle; this plugin only reads and writes files and shells out to the `herdr` CLI.

**Tech Stack:** Go 1.27, Bubble Tea + Lipgloss for the TUI, stdlib for everything else. No JSON schema libraries — transcripts round-trip through `map[string]any` to preserve unknown fields.

**Spec:** `docs/superpowers/specs/2026-09-21-herdr-tree-design.md`

## Global Constraints

- Go 1.27. Module path `herdr-tree` (local; change when published).
- `min_herdr_version = "0.9.0"`. Plugin id `herdr-tree`. Platforms: linux, macos.
- Grafted transcripts MUST be written with mode `0600`, explicitly — never left to umask.
- All writes into `~/.claude/projects/` and the store are tmp-then-rename.
- JSON decoding of transcript entries MUST use `Decoder.UseNumber()` — a float round-trip corrupts large integer fields.
- The core (`internal/tree`, `internal/store`, `internal/tui`) must never import `internal/claude` or parse JSONL. Only `cmd/herdr-tree` wires a concrete adapter in.
- Never log message content. Ids, counts, and paths only.
- Never delete a session file the user could still resume, including orphans from a failed branch.
- Verified against Claude Code 2.1.278. Record the observed `version` field; refuse to graft on mismatch rather than guess.

---

### Task 1: Skeleton, neutral types, repo root resolution

**Files:**
- Create: `go.mod`, `internal/adapter/adapter.go`, `internal/repo/repo.go`
- Test: `internal/repo/repo_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `adapter.Node`, `adapter.Session`, `adapter.Pane`, `adapter.Adapter` (with `Name`, `Discover`, `Current`, `Preview`, `Branch`, `Resume`); `repo.Root(dir string) (root string, isGit bool, err error)`.

- [ ] **Step 1: Initialize the repository and module**

This project directory is not yet a git repo, and every task ends in a commit.

```bash
cd /home/somliga/projects/herdr-tree
git init
go mod init herdr-tree
printf 'bin/\n' > .gitignore
```

- [ ] **Step 2: Write the neutral types**

Create `internal/adapter/adapter.go`:

```go
// Package adapter defines the agent-neutral types and the interface every
// agent adapter implements. Nothing in this package may know about Claude.
package adapter

import "time"

// Node is one conversation turn.
type Node struct {
	ID    string // stable id of the turn within its session
	Title string // single-line label, already truncated
	At    time.Time
}

// Session is one agent session: a linear path of turns.
type Session struct {
	ID      string
	CWD     string
	Title   string
	Updated time.Time
	Nodes   []Node // ordered root -> leaf
	Live    bool   // a process currently holds it
	Broken  bool   // transcript present but unreadable
}

// Pane is what Herdr reports about the invoking pane.
type Pane struct {
	ID             string
	CWD            string
	Agent          string // agent kind Herdr detected, e.g. "claude"
	AgentSessionID string // Herdr's belief about the session; a hint only
}

// Adapter is the whole agent-specific surface.
type Adapter interface {
	Name() string
	Discover(repoRoot string) ([]Session, error)
	Current(p Pane) (sessionID string, err error)
	// Preview reports what a graft at atNode would carry, for the
	// confirmation dialog. Computed without writing anything.
	Preview(src Session, atNode string) (turns, entries int, size int64, err error)
	Branch(src Session, atNode, dstCWD string) (newSessionID string, err error)
	Resume(sessionID, cwd string) error
}
```

- [ ] **Step 3: Write the failing test for repo root resolution**

Create `internal/repo/repo_test.go`:

```go
package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRootOfPlainRepo(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init")
	got, isGit, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !isGit {
		t.Fatal("want isGit true")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWorktreeFoldsIntoMainRepo(t *testing.T) {
	main := t.TempDir()
	git(t, main, "init")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "f")
	git(t, main, "commit", "-m", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, main, "worktree", "add", "-b", "side", wt)

	got, isGit, err := Root(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !isGit {
		t.Fatal("want isGit true")
	}
	want, _ := filepath.EvalSymlinks(main)
	if got != want {
		t.Fatalf("worktree resolved to %q, want main repo %q", got, want)
	}
}

func TestRemovedWorktreeStillResolvesToItsRepo(t *testing.T) {
	main := t.TempDir()
	git(t, main, "init")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "f")
	git(t, main, "commit", "-m", "init")

	// A worktree inside the repo, then deleted — the everyday case.
	wt := filepath.Join(main, ".worktrees", "gone")
	git(t, main, "worktree", "add", "-b", "side", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	got, isGit, err := Root(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !isGit {
		t.Fatal("a removed worktree must still resolve to its repo, or its sessions vanish from the tree")
	}
	want, _ := filepath.EvalSymlinks(main)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRemovedDirOutsideAnyRepoDoesNotMisattribute(t *testing.T) {
	base := t.TempDir()
	gone := filepath.Join(base, "never-existed", "deeper")
	got, isGit, err := Root(gone)
	if err != nil {
		t.Fatal(err)
	}
	if isGit {
		t.Fatalf("walked up into a repo that never contained %q: got %q", gone, got)
	}
}

func TestNonRepoReturnsDirItself(t *testing.T) {
	dir := t.TempDir()
	got, isGit, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if isGit {
		t.Fatal("want isGit false")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/repo/ -run Test -v`
Expected: FAIL — `undefined: Root`.

- [ ] **Step 5: Implement repo.Root**

Create `internal/repo/repo.go`:

```go
// Package repo resolves a working directory to the repository that owns it,
// so that every git worktree of one repo shares a single tree.
package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Root returns the directory that owns dir. For a git worktree this is the
// main repository, so all worktrees group together. For a directory that is
// not in a repository it returns the directory itself with isGit false,
// which is a supported mode, not an error.
//
// dir need not still exist. A removed git worktree is ordinary in this
// workflow, and the sessions recorded inside it are exactly the history a
// user wants to look back at. When dir is gone, Root walks up to the nearest
// surviving ancestor and resolves that instead, so those sessions stay
// attributed to their repository rather than silently vanishing from the
// tree. The walk cannot mis-attribute: it only ever yields a repository that
// genuinely contains the missing path.
func Root(dir string) (root string, isGit bool, err error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}

	probe := resolved
	for {
		if fi, e := os.Stat(probe); e == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe { // reached the filesystem root
			return resolved, false, nil
		}
		probe = parent
	}

	cmd := exec.Command("git", "-C", probe,
		"rev-parse", "--path-format=absolute", "--git-common-dir")
	out, gerr := cmd.Output()
	if gerr != nil {
		return resolved, false, nil
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return resolved, false, nil
	}
	r := filepath.Dir(common)
	if rr, e := filepath.EvalSymlinks(r); e == nil {
		r = rr
	}
	return r, true, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/repo/ -v`
Expected: PASS, three tests.

- [ ] **Step 7: Commit**

```bash
git add go.mod .gitignore internal/adapter/adapter.go internal/repo/
git commit -m "$(cat <<'EOF'
feat: neutral adapter types and repo root resolution

Worktrees fold into their main repo so one tree spans them all.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Transcript entry parsing with field fidelity

**Files:**
- Create: `internal/claude/entry.go`, `internal/claude/testdata/simple.jsonl`
- Test: `internal/claude/entry_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `claude.Entry` with fields `Raw map[string]any`, and methods `Type() string`, `UUID() string`, `ParentUUID() string`, `RequestID() string`, `IsSidechain() bool`, `HasToolUseResult() bool`, `Text() string`, `Timestamp() time.Time`, `CWD() string`, `SessionID() string`, `Version() string`, `AITitle() string`; `claude.ParseFile(path string) (entries []Entry, skipped int, err error)`; `claude.Marshal(e Entry) ([]byte, error)`.

Entries must survive a decode/encode round-trip with unknown fields intact, because grafting rewrites three fields and copies everything else verbatim.

- [ ] **Step 1: Create the shared fixture**

Create `internal/claude/testdata/simple.jsonl`. Every later task reuses it. One JSON object per line, exactly as written:

```
{"type":"user","uuid":"u1","parentUuid":null,"sessionId":"S","cwd":"/repo","version":"2.1.278","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"first question"}]}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"S","requestId":"r1","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]}}
{"type":"assistant","uuid":"a2","parentUuid":"a1","sessionId":"S","requestId":"r1","timestamp":"2026-01-01T10:00:06Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}
{"type":"attachment","uuid":"at1","parentUuid":"u1","sessionId":"S","timestamp":"2026-01-01T10:00:07Z","attachment":{"kind":"reminder"}}
{"type":"user","uuid":"tr1","parentUuid":"a2","sessionId":"S","timestamp":"2026-01-01T10:00:08Z","toolUseResult":{"ok":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]}}
{"type":"user","uuid":"u2","parentUuid":"tr1","sessionId":"S","timestamp":"2026-01-01T10:00:09Z","message":{"role":"user","content":[{"type":"text","text":"Continue from where you left off."}]}}
{"type":"user","uuid":"u3","parentUuid":"u2","sessionId":"S","timestamp":"2026-01-01T10:00:10Z","message":{"role":"user","content":[{"type":"text","text":"second question\nwith a second line"}]}}
{"type":"assistant","uuid":"a3","parentUuid":"u3","sessionId":"S","requestId":"r2","timestamp":"2026-01-01T10:00:11Z","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]}}
{"type":"user","uuid":"sc1","parentUuid":"a3","sessionId":"S","isSidechain":true,"timestamp":"2026-01-01T10:00:12Z","message":{"role":"user","content":[{"type":"text","text":"subagent prompt"}]}}
{"type":"last-prompt","leafUuid":"a3","sessionId":"S"}
{"type":"ai-title","aiTitle":"Fixture session","sessionId":"S"}
```

- [ ] **Step 2: Write the failing test**

Create `internal/claude/entry_test.go`:

```go
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileReadsEveryLine(t *testing.T) {
	es, _, _, err := ParseFile("testdata/simple.jsonl")
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
	es, _, _ := ParseFile("testdata/simple.jsonl")
	if got := es[0].Text(); got != "first question" {
		t.Fatalf("got %q", got)
	}
	if got := es[6].Text(); got != "second question\nwith a second line" {
		t.Fatalf("got %q", got)
	}
}

func TestParseFileCountsSkippedLines(t *testing.T) {
	dir := t.TempDir()
	good := `{"type":"user","uuid":"u1"}`
	path := filepath.Join(dir, "s.jsonl")
	// one good line, one truncated mid-write, one that is not an object
	body := good + "\n" + `{"type":"user","uuid":` + "\nnot json\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	es, skipped, _, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 {
		t.Fatalf("entries %d want 1", len(es))
	}
	if skipped != 2 {
		t.Fatalf("skipped %d want 2 — callers rely on this to tell a partial transcript from a clean one", skipped)
	}
}

func TestParseFileReportsZeroSkippedForCleanFile(t *testing.T) {
	_, skipped, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("clean fixture reported %d skipped lines", skipped)
	}
}

func TestRoundTripPreservesUnknownFields(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
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
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/claude/ -v`
Expected: FAIL — `undefined: ParseFile`.

- [ ] **Step 4: Implement the entry type**

Create `internal/claude/entry.go`:

```go
// Package claude is the Claude Code adapter. Every detail of Claude's
// on-disk transcript format lives here and nowhere else.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Entry is one line of a transcript. It keeps the decoded object so that
// grafting can rewrite a few fields and copy everything else verbatim,
// including fields this program has never heard of.
type Entry struct {
	Raw map[string]any
}

func (e Entry) str(k string) string {
	if v, ok := e.Raw[k].(string); ok {
		return v
	}
	return ""
}

func (e Entry) Type() string       { return e.str("type") }
func (e Entry) UUID() string       { return e.str("uuid") }
func (e Entry) ParentUUID() string { return e.str("parentUuid") }
func (e Entry) RequestID() string  { return e.str("requestId") }
func (e Entry) SessionID() string  { return e.str("sessionId") }
func (e Entry) CWD() string        { return e.str("cwd") }
func (e Entry) Version() string    { return e.str("version") }
func (e Entry) AITitle() string    { return e.str("aiTitle") }

func (e Entry) IsSidechain() bool {
	b, _ := e.Raw["isSidechain"].(bool)
	return b
}

func (e Entry) HasToolUseResult() bool {
	_, ok := e.Raw["toolUseResult"]
	return ok
}

func (e Entry) Timestamp() time.Time {
	t, err := time.Parse(time.RFC3339, e.str("timestamp"))
	if err != nil {
		return time.Time{}
	}
	return t
}

// Text returns the entry's human-readable text. Content may be a bare
// string or a list of blocks; only text blocks contribute.
func (e Entry) Text() string {
	msg, ok := e.Raw["message"].(map[string]any)
	if !ok {
		return ""
	}
	switch c := msg["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			blk, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := blk["text"].(string); ok && blk["type"] == "text" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// IsToolResult reports whether the entry's content is a tool result block.
func (e Entry) IsToolResult() bool {
	msg, ok := e.Raw["message"].(map[string]any)
	if !ok {
		return false
	}
	blocks, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, b := range blocks {
		if blk, ok := b.(map[string]any); ok && blk["type"] == "tool_result" {
			return true
		}
	}
	return false
}

func parseLine(line []byte) (Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber() // large ints must not become float64
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return Entry{}, err
	}
	return Entry{Raw: m}, nil
}

// Marshal re-encodes an entry. json.Number keeps integers byte-identical.
func Marshal(e Entry) ([]byte, error) { return json.Marshal(e.Raw) }

// ParseFile reads a transcript. A malformed line is skipped rather than
// failing the whole session, because a transcript being written right now is
// legitimately truncated mid-line. The count of skipped lines is returned so
// callers can tell "still being written" from "corrupt": Discover marks such
// a session Broken, and Graft refuses it outright, because a dropped line can
// break the parentUuid chain and silently produce a graft with the wrong
// history.
func ParseFile(path string) (entries []Entry, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // entries can be large
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			skipped++
			continue
		}
		e, perr := parseLine(line)
		if perr != nil {
			skipped++
			continue
		}
		out = append(out, e)
	}
	return out, skipped, sc.Err()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -v`
Expected: PASS, four tests.

- [ ] **Step 6: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: transcript entry parsing with field fidelity

Entries round-trip through map[string]any with UseNumber so grafting can
rewrite three fields and copy the rest verbatim.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Turn extraction and titles

**Files:**
- Create: `internal/claude/turns.go`
- Test: `internal/claude/turns_test.go`

**Interfaces:**
- Consumes: `claude.Entry` and its methods from Task 2.
- Produces: `claude.Turns(entries []Entry) []adapter.Node`; `claude.Title(text string, max int) string`; `claude.SessionTitle(entries []Entry) string`.

The filter is the spec's, and each clause was found the hard way. `"Continue from where you left off."` is injected by Claude Code on every resume and is indistinguishable from a real prompt by type alone.

- [ ] **Step 1: Write the failing test**

Create `internal/claude/turns_test.go`:

```go
package claude

import "testing"

func TestTurnsFiltersNonPrompts(t *testing.T) {
	es, _, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got := Turns(es)
	var ids []string
	for _, n := range got {
		ids = append(ids, n.ID)
	}
	want := []string{"u1", "u3"}
	if len(ids) != len(want) {
		t.Fatalf("got %v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v want %v", ids, want)
		}
	}
}

func TestTurnsExcludeSlashCommandMachinery(t *testing.T) {
	// Claude Code writes a caveat banner, the command name and its captured
	// stdout as user entries around every slash command. They are real
	// transcript content — Select keeps them — but they are not turns anyone
	// would branch from, and they were the first three rows of most sessions.
	lines := []string{
		`{"type":"user","uuid":"m1","parentUuid":null,"sessionId":"S","message":{"role":"user","content":[{"type":"text","text":"<local-command-caveat>Caveat: the messages below were generated by the user</local-command-caveat>"}]}}`,
		`{"type":"user","uuid":"m2","parentUuid":"m1","sessionId":"S","message":{"role":"user","content":[{"type":"text","text":"<command-name>/model</command-name>"}]}}`,
		`{"type":"user","uuid":"m3","parentUuid":"m2","sessionId":"S","message":{"role":"user","content":[{"type":"text","text":"<local-command-stdout>Set model to opus</local-command-stdout>"}]}}`,
		`{"type":"user","uuid":"m4","parentUuid":"m3","sessionId":"S","message":{"role":"user","content":[{"type":"text","text":"a real question"}]}}`,
	}
	var es []Entry
	for _, l := range lines {
		e, err := parseLine([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}
	got := Turns(es)
	if len(got) != 1 || got[0].ID != "m4" {
		var ids []string
		for _, n := range got {
			ids = append(ids, n.ID)
		}
		t.Fatalf("got %v want only [m4]", ids)
	}
	// but grafting must still carry them: they are transcript content
	keep, err := Select(es, "m4")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"m1", "m2", "m3"} {
		if !keep[u] {
			t.Fatalf("Select dropped %s; hiding a row from the tree must not delete it from the transcript", u)
		}
	}
}

func TestTurnTitleIsFirstLineTruncated(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	got := Turns(es)
	if got[1].Title != "second question" {
		t.Fatalf("got %q", got[1].Title)
	}
}

func TestTitleTruncation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"short", "short"},
		{"", ""},
		{"\n\n  leading blanks", "leading bl…"},
		{"0123456789abcdef", "0123456789…"},
	}
	for _, c := range cases {
		if got := Title(c.in, 11); got != c.want {
			t.Fatalf("Title(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestSessionTitlePrefersAITitle(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	if got := SessionTitle(es); got != "Fixture session" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/ -run 'Turn|Title' -v`
Expected: FAIL — `undefined: Turns`.

- [ ] **Step 3: Implement turn extraction**

Create `internal/claude/turns.go`:

```go
package claude

import (
	"strings"

	"herdr-tree/internal/adapter"
)

// syntheticResume is written by Claude Code as a user entry whenever a
// session is resumed. It is not something the user typed.
const syntheticResume = "Continue from where you left off."

// machinery marks user entries that Claude Code writes around a slash
// command — the caveat banner, the command name, its captured stdout. They
// are real transcript content, so Select keeps them, but they are not turns
// anyone would branch from and they crowd the top of almost every session.
// Measured on this machine: 277 of 3664 rendered turns, 7.6%.
var machinery = []string{
	"<local-command-caveat",
	"<command-name>",
	"<command-message>",
	"<local-command-stdout",
	"<user-memory-input",
}

// IsPrompt reports whether an entry is a turn the user actually took.
func IsPrompt(e Entry) bool {
	if e.Type() != "user" || e.IsSidechain() {
		return false
	}
	if e.HasToolUseResult() || e.IsToolResult() {
		return false
	}
	t := strings.TrimSpace(e.Text())
	if t == syntheticResume {
		return false
	}
	for _, m := range machinery {
		if strings.HasPrefix(t, m) {
			return false
		}
	}
	return true
}

// Turns returns the session's user turns in file order.
func Turns(es []Entry) []adapter.Node {
	var out []adapter.Node
	for _, e := range es {
		if !IsPrompt(e) {
			continue
		}
		out = append(out, adapter.Node{
			ID:    e.UUID(),
			Title: Title(e.Text(), 72),
			At:    e.Timestamp(),
		})
	}
	return out
}

// Title reduces text to one line of at most max runes, ellipsis included.
func Title(text string, max int) string {
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			line = strings.TrimSpace(l)
			break
		}
	}
	r := []rune(line)
	if len(r) <= max {
		return line
	}
	if max < 1 {
		return ""
	}
	return string(r[:max-1]) + "…"
}

// SessionTitle prefers the ai-title Claude Code writes, falling back to the
// first turn.
func SessionTitle(es []Entry) string {
	title := ""
	for _, e := range es {
		if e.Type() == "ai-title" && e.AITitle() != "" {
			title = e.AITitle()
		}
	}
	if title != "" {
		return Title(title, 72)
	}
	for _, e := range es {
		if IsPrompt(e) {
			return Title(e.Text(), 72)
		}
	}
	return "(empty session)"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -v`
Expected: PASS, all tests including Task 2's.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: turn extraction and titles

Filters tool results, sidechains, and the synthetic resume turn Claude Code
injects, which is otherwise indistinguishable from a real prompt.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Session discovery and repo grouping

**Files:**
- Create: `internal/claude/discover.go`
- Test: `internal/claude/discover_test.go`

**Interfaces:**
- Consumes: `ParseFile`, `Turns`, `SessionTitle` (Tasks 2-3); `repo.Root` (Task 1).
- Produces: `claude.ProjectsDir() string`; `claude.Discover(repoRoot string) ([]adapter.Session, error)`; `claude.SlugFor(cwd string) string`.

Claude Code slugs a cwd by replacing `/` with `-`. That mapping is lossy, so a session's real cwd is read from its first entry carrying one, never inferred from the directory name.

- [ ] **Step 1: Write the failing test**

Create `internal/claude/discover_test.go`:

```go
package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSession copies the fixture into a fake projects tree, rewriting cwd.
// The slug is derived with SlugFor so the file lands exactly where
// TranscriptPath will later look for it.
func writeSession(t *testing.T, projects, id, cwd string) {
	t.Helper()
	es, _, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(projects, SlugFor(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range es {
		if _, ok := e.Raw["cwd"]; ok {
			e.Raw["cwd"] = cwd
		}
		e.Raw["sessionId"] = id
		b, _ := Marshal(e)
		f.Write(append(b, '\n'))
	}
}

func TestSlugFor(t *testing.T) {
	// Verified against Claude Code 2.1.278: every non-alphanumeric rune
	// becomes "-", per rune rather than per byte. Writing a graft into the
	// wrong directory produces a session Claude Code can never find.
	cases := []struct{ in, want string }{
		{"/home/a/projects/x", "-home-a-projects-x"},
		{"/home/a/doc writing", "-home-a-doc-writing"},
		{"/home/a/slug_test.dir v2+x", "-home-a-slug-test-dir-v2-x"},
		{"/home/a/Solör Bioenergi", "-home-a-Sol-r-Bioenergi"},
		{"/home/a/keeps-dashes", "-home-a-keeps-dashes"},
	}
	for _, c := range cases {
		if got := SlugFor(c.in); got != c.want {
			t.Fatalf("SlugFor(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestDiscoverCarriesTheDiscoveredPath(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	id := "11111111-1111-4111-8111-111111111111"
	writeSession(t, projects, id, repoDir)

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(projects, SlugFor(repoDir), id+".jsonl")
	if got[0].Path != want {
		t.Fatalf("Path = %q want %q", got[0].Path, want)
	}
}

func TestDiscoverFindsASessionWhoseCWDDoesNotMatchItsDirectory(t *testing.T) {
	// A session that relocated into a worktree keeps its original cwd while
	// its transcript lives under a differently named project directory.
	// Reconstructing the path from the cwd would miss it entirely.
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	id := "22222222-2222-4222-8222-222222222222"

	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(projects, "-some-unrelated-worktree-name")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(odd, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if _, ok := e.Raw["cwd"]; ok {
			e.Raw["cwd"] = repoDir
		}
		e.Raw["sessionId"] = id
		b, _ := Marshal(e)
		f.Write(append(b, '\n'))
	}
	f.Close()

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions %d want 1", len(got))
	}
	if got[0].Path != filepath.Join(odd, id+".jsonl") {
		t.Fatalf("Path = %q; must be where the file WAS FOUND, not where its cwd implies", got[0].Path)
	}
}

func TestDiscoverGroupsByRepoRoot(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)

	repoDir := t.TempDir()
	other := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)
	writeSession(t, projects, "22222222-2222-4222-8222-222222222222", other)

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions want 1", len(got))
	}
	s := got[0]
	if s.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("id %q", s.ID)
	}
	if s.CWD != repoDir {
		t.Fatalf("cwd %q want %q", s.CWD, repoDir)
	}
	if len(s.Nodes) != 2 {
		t.Fatalf("nodes %d want 2", len(s.Nodes))
	}
	if s.Title != "Fixture session" {
		t.Fatalf("title %q", s.Title)
	}
}

func TestDiscoverSkipsOtherReposWithoutFullyParsingThem(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	mine := t.TempDir()
	theirs := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", mine)
	writeSession(t, projects, "22222222-2222-4222-8222-222222222222", theirs)

	// Corrupt the OTHER repo's transcript beyond the head. A full parse would
	// still succeed, but the cheap head check must reject it before we get
	// there — and the result must be identical either way.
	other := filepath.Join(projects, SlugFor(theirs), "22222222-2222-4222-8222-222222222222.jsonl")
	b, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, append(b, []byte("not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CWD != mine {
		t.Fatalf("got %d sessions, want only the one in this repo: %+v", len(got), got)
	}
	if got[0].Broken {
		t.Fatal("our own clean session must not be marked Broken")
	}
}

func TestDiscoverMarksPartialTranscriptBroken(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()

	id := "11111111-1111-4111-8111-111111111111"
	writeSession(t, projects, id, repoDir)

	// Append a line truncated mid-write, exactly as a transcript being
	// appended to right now would look.
	p := filepath.Join(projects, SlugFor(repoDir), id+".jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","uuid":` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions %d want 1", len(got))
	}
	if !got[0].Broken {
		t.Fatal("a transcript with unparseable lines must be Broken, so the tree shows the warning row instead of rendering a partial conversation as complete")
	}
}

func TestDiscoverLeavesCleanSessionUnbroken(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Broken {
		t.Fatalf("clean session must not be Broken: %+v", got)
	}
}

func TestDiscoverExcludesWhollyUnreadableSession(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)
	// a file with no parseable entry at all
	bad := filepath.Join(projects, SlugFor(repoDir), "33333333-3333-4333-8333-333333333333.jsonl")
	if err := os.WriteFile(bad, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("an unreadable session must not join a repo it cannot claim: got %d", len(got))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/ -run Discover -v`
Expected: FAIL — `undefined: Discover`.

- [ ] **Step 3: Implement discovery**

Create `internal/claude/discover.go`:

```go
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/repo"
)

// ProjectsDir is where Claude Code keeps transcripts. The environment
// variable exists so tests can point somewhere else.
func ProjectsDir() string {
	if d := os.Getenv("CLAUDE_PROJECTS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// SlugFor is Claude Code's directory name for a working directory: every
// rune that is not a letter or digit becomes "-".
//
// Verified empirically against Claude Code 2.1.278 by running it in a
// directory named `slug_test.dir v2+x`, which produced `slug-test-dir-v2-x`:
// "/", "_", ".", " " and "+" all collapse to "-". It is per RUNE, not per
// byte — a real transcript here shows `Solör Bioenergi` becoming
// `Sol-r-Bioenergi`, one dash for a two-byte character.
//
// Getting this wrong is not cosmetic: a graft written into the wrong
// directory is a session Claude Code will never find, so the branch silently
// cannot be resumed.
func SlugFor(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// sessionCWD returns the first cwd recorded in a transcript. The directory
// name is a lossy encoding of the path, so it is never used for this.
func sessionCWD(es []Entry) string {
	for _, e := range es {
		if c := e.CWD(); c != "" {
			return c
		}
	}
	return ""
}

// headCWD reads only far enough to find the first cwd, so Discover can reject
// a transcript belonging to another repository without decoding all of it.
// Most of the cost of opening the tree was parsing megabytes of sessions that
// were then discarded: on this machine one repo matched 54 of 104 files.
func headCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 0; n < 200 && sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var probe struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(line, &probe) == nil && probe.CWD != "" {
			return probe.CWD
		}
	}
	return ""
}

// Discover returns every session belonging to repoRoot, newest first.
func Discover(repoRoot string) ([]adapter.Session, error) {
	pattern := filepath.Join(ProjectsDir(), "*", "*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	var out []adapter.Session
	for _, p := range paths {
		es, skipped, _, err := ParseFile(p)
		if err != nil || len(es) == 0 {
			continue // unreadable: cannot be attributed to any repo
		}
		cwd := sessionCWD(es)
		if cwd == "" {
			continue
		}
		root, _, err := repo.Root(cwd)
		if err != nil || root != repoRoot {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		st, serr := os.Stat(p)
		var updated = es[len(es)-1].Timestamp()
		if serr == nil {
			updated = st.ModTime()
		}
		out = append(out, adapter.Session{
			ID:      id,
			CWD:     cwd,
			Path:    p,
			Title:   SessionTitle(es),
			Updated: updated,
			Nodes:   Turns(es),
			// A skipped line means the chain may have holes. Surface it as ⚠
			// rather than rendering a partial conversation as if complete.
			Broken: skipped > 0,
		})
	}
	// Stable so sessions with identical mtimes keep a deterministic order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: session discovery grouped by repo root

A session's cwd is read from its entries, never inferred from the project
directory name, which is a lossy encoding.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Graft selection — the golden test

**Files:**
- Create: `internal/claude/graft.go`
- Test: `internal/claude/graft_test.go`

**Interfaces:**
- Consumes: `claude.Entry` (Task 2).
- Produces: `claude.Select(es []Entry, atNode string) (map[string]bool, error)`; `claude.ErrNodeNotFound`.

This is the one piece of logic that can be wrong in a way that looks right. A graft that keeps too little silently truncates the user's history; one that keeps too much silently includes a branch they rejected. Three rules, each established empirically:

1. The ancestor chain of the chosen node, via `parentUuid`.
2. Assistant entries sharing a kept entry's `requestId` — one response is written as several entries, one per content block.
3. `attachment` entries whose parent is kept.

Note the synthetic resume turn IS kept here. It is filtered from the *tree view* because the user did not type it, but it is genuine conversation content and removing it would corrupt the transcript.

- [ ] **Step 1: Write the failing test**

Create `internal/claude/graft_test.go`:

```go
package claude

import (
	"sort"
	"testing"
)

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSelectKeepsChainSiblingsAndAttachments(t *testing.T) {
	es, _, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Select(es, "u3")
	if err != nil {
		t.Fatal(err)
	}
	// chain u3<-u2<-tr1<-a2<-u1, plus a1 (shares requestId r1 with a2),
	// plus at1 (attachment child of u1).
	want := []string{"a1", "a2", "at1", "tr1", "u1", "u2", "u3"}
	g := keys(got)
	if len(g) != len(want) {
		t.Fatalf("got %v want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v want %v", g, want)
		}
	}
}

func TestSelectExcludesDescendantsAndSidechains(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	got, _ := Select(es, "u3")
	for _, bad := range []string{"a3", "sc1"} {
		if got[bad] {
			t.Fatalf("%s must not be kept when grafting at u3", bad)
		}
	}
}

func TestSelectAtRootKeepsOnlyRootAndItsSiblings(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	got, _ := Select(es, "u1")
	want := []string{"at1", "u1"}
	g := keys(got)
	if len(g) != len(want) || g[0] != want[0] || g[1] != want[1] {
		t.Fatalf("got %v want %v", g, want)
	}
}

func TestSelectKeepsEveryToolResultOfAParallelCall(t *testing.T) {
	es, _, err := ParseFile("testdata/parallel.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Select(es, "u2")
	if err != nil {
		t.Fatal(err)
	}
	// Chain: u2 <- a3 <- tr1 <- a1 <- u1. a2 arrives by requestId r1. tr2 is a
	// SIBLING result, reachable only by rule 4 — and a2's tool_use t2 is
	// meaningless without it.
	want := []string{"a1", "a2", "a3", "tr1", "tr2", "u1", "u2"}
	g := keys(got)
	if len(g) != len(want) {
		t.Fatalf("got %v want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v want %v", g, want)
		}
	}
}

func TestGraftLeavesNoToolUseWithoutItsResult(t *testing.T) {
	// The invariant that actually matters: every tool_use kept must have its
	// tool_result kept too. Claude Code's own transcripts satisfy this; a
	// graft that breaks it writes a conversation shape that cannot exist.
	for _, fixture := range []string{"testdata/parallel.jsonl", "testdata/simple.jsonl"} {
		es, _, err := ParseFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if !IsPrompt(e) {
				continue
			}
			keep, err := Select(es, e.UUID())
			if err != nil {
				t.Fatal(err)
			}
			uses, results := map[string]bool{}, map[string]bool{}
			for _, k := range es {
				if k.UUID() == "" || !keep[k.UUID()] {
					continue
				}
				m, _ := k.Raw["message"].(map[string]any)
				if m == nil {
					continue
				}
				blocks, _ := m["content"].([]any)
				for _, b := range blocks {
					blk, ok := b.(map[string]any)
					if !ok {
						continue
					}
					if blk["type"] == "tool_use" {
						if id, ok := blk["id"].(string); ok {
							uses[id] = true
						}
					}
					if blk["type"] == "tool_result" {
						if id, ok := blk["tool_use_id"].(string); ok {
							results[id] = true
						}
					}
				}
			}
			for id := range uses {
				if !results[id] {
					t.Fatalf("%s: grafting at %s orphaned tool_use %q", fixture, e.UUID(), id)
				}
			}
		}
	}
}

func TestSelectDoesNotKeepWorkFromAfterTheGraftPoint(t *testing.T) {
	// An assistant turn AFTER the branch point shares nothing with the chain,
	// but an unbounded requestId sweep can still reach it. Nothing authored
	// after the branch belongs in the graft.
	es, _, err := ParseFile("testdata/parallel.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Append a later turn whose response reuses requestId r1.
	later := []string{
		`{"type":"user","uuid":"u3","parentUuid":"u2","sessionId":"S","timestamp":"2026-01-01T10:00:07Z","message":{"role":"user","content":[{"type":"text","text":"third"}]}}`,
		`{"type":"assistant","uuid":"a9","parentUuid":"u3","sessionId":"S","requestId":"r1","timestamp":"2026-01-01T10:00:08Z","message":{"role":"assistant","content":[{"type":"text","text":"later work"}]}}`,
	}
	for _, l := range later {
		e, err := parseLine([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}

	keep, err := Select(es, "u2")
	if err != nil {
		t.Fatal(err)
	}
	if keep["a9"] {
		t.Fatal("kept an assistant entry written after the graft point: that is work the user branched away from")
	}
	if keep["u3"] {
		t.Fatal("kept a prompt from after the graft point")
	}
	if !keep["a2"] {
		t.Fatal("the bound must not drop the legitimate same-turn sibling a2")
	}
}

func TestSelectUnknownNode(t *testing.T) {
	es, _, _ := ParseFile("testdata/simple.jsonl")
	if _, err := Select(es, "nope"); err != ErrNodeNotFound {
		t.Fatalf("got %v want ErrNodeNotFound", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/ -run Select -v`
Expected: FAIL — `undefined: Select`.

- [ ] **Step 3: Implement selection**

Create `internal/claude/graft.go`:

```go
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
// Rule 4: tool_result entries whose parent is kept.
//
// Rule 4 is not symmetry for its own sake. Claude Code writes a parallel tool
// call as a CHAIN of assistant entries, one per tool_use block, and then one
// tool_result per tool, each parented to its own tool_use. Only one of those
// results lies on the linear parentUuid chain; the rest are siblings. Without
// this rule the graft keeps every tool_use (they are all ancestors) while
// dropping the sibling results, producing assistant turns whose tool_use
// blocks have no answer — a shape Claude Code never writes and the Messages
// API rejects. Measured across every graft point in 103 real transcripts on
// the development machine: 48.3% of grafts orphaned at least one tool_use,
// 12340 blocks in total, against an orphan rate of 0.03% in the source files.
//
// The synthetic "Continue from where you left off." turn is kept: it is
// real conversation content, and only the tree view hides it.
func Select(es []Entry, atNode string) (map[string]bool, error) {
	byUUID := make(map[string]Entry, len(es))
	order := make(map[string]int, len(es))
	for i, e := range es {
		if u := e.UUID(); u != "" {
			byUUID[u] = e
			order[u] = i
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

	// Rule 2, bounded to entries at or before the graft point.
	//
	// A requestId identifies a whole assistant turn, so an unbounded sweep can
	// pull in entries written AFTER the branch — importing work the user chose
	// to prune. Measured across every graft point in 103 real transcripts: 14
	// assistant and 14 user entries were captured this way. Small, but it is
	// content from a branch the user deliberately left behind, which is the
	// opposite of what this function is for.
	//
	// The bound is deliberately on rule 2 only. Rule 3's attachments are
	// system-reminders injected WITH a kept prompt and hang off it as
	// children, so they are legitimately part of that turn even though they
	// are written later — 2828 of them across the same corpus.
	at, ok := order[atNode]
	if !ok {
		return nil, ErrNodeNotFound
	}
	reqs := map[string]bool{}
	for u := range keep {
		if e := byUUID[u]; e.Type() == "assistant" && e.RequestID() != "" {
			reqs[e.RequestID()] = true
		}
	}
	for _, e := range es {
		if e.Type() == "assistant" && e.RequestID() != "" && reqs[e.RequestID()] {
			if u := e.UUID(); u != "" && order[u] <= at {
				keep[u] = true
			}
		}
	}

	// Rules 3 and 4, iterated to a fixpoint.
	//
	// A single file-order pass happens to work for attachments only because
	// Claude Code writes them after their parent. That is an accident of the
	// format, not a guarantee, and a tool_result recovered by rule 4 can
	// itself have attachment children. Looping until nothing new is added
	// removes the dependency on write order entirely. The corpus converges in
	// two passes; the loop is bounded by the entry count regardless.
	for {
		added := false
		for _, e := range es {
			u := e.UUID()
			if u == "" || keep[u] || !keep[e.ParentUUID()] {
				continue
			}
			if e.Type() == "attachment" || (e.Type() == "user" && e.IsToolResult()) {
				keep[u] = true
				added = true
			}
		}
		if !added {
			break
		}
	}

	return keep, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -run Select -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: graft selection with golden test

Keeps the ancestor chain, requestId siblings, and attachment children. Each
rule was established against real transcripts; a naive parentUuid walk keeps
too little and renders a truncated conversation.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Graft write

**Files:**
- Modify: `internal/claude/graft.go`
- Test: `internal/claude/graft_write_test.go`

**Interfaces:**
- Consumes: `Select` (Task 5), `Marshal`, `ParseFile` (Task 2), `SlugFor`, `ProjectsDir` (Task 4).
- Produces: `claude.Graft(srcPath, atNode, dstCWD string) (newSessionID, dstPath string, err error)`; `claude.ErrUnsupportedVersion`; `claude.ErrPartialTranscript`.

- [ ] **Step 1: Write the failing test**

Create `internal/claude/graft_write_test.go`:

```go
package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraftWritesResumableSession(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	dstCWD := t.TempDir()

	srcBefore, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	sid, dst, err := Graft("testdata/simple.jsonl", "u3", dstCWD)
	if err != nil {
		t.Fatal(err)
	}
	if sid == "" {
		t.Fatal("no session id returned")
	}
	if want := filepath.Join(projects, SlugFor(dstCWD), sid+".jsonl"); dst != want {
		t.Fatalf("dst %q want %q", dst, want)
	}

	srcAfter, _ := os.ReadFile("testdata/simple.jsonl")
	if string(srcBefore) != string(srcAfter) {
		t.Fatal("source transcript was modified")
	}

	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v want 0600 — conversation content must not be world readable", fi.Mode().Perm())
	}

	es, _, _, err := ParseFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var last Entry
	seen := map[string]bool{}
	for _, e := range es {
		if u := e.UUID(); u != "" {
			seen[u] = true
		}
		if e.SessionID() != "" && e.SessionID() != sid {
			t.Fatalf("entry kept old sessionId %q", e.SessionID())
		}
		if c := e.CWD(); c != "" && c != dstCWD {
			t.Fatalf("entry kept old cwd %q", c)
		}
		last = e
	}
	for _, want := range []string{"u1", "a1", "a2", "at1", "tr1", "u2", "u3"} {
		if !seen[want] {
			t.Fatalf("missing kept entry %s", want)
		}
	}
	for _, bad := range []string{"a3", "sc1"} {
		if seen[bad] {
			t.Fatalf("descendant %s leaked into graft", bad)
		}
	}
	if last.Type() != "last-prompt" || last.Raw["leafUuid"] != "u3" {
		t.Fatalf("last entry should be the leaf pointer, got %v", last.Raw)
	}
	for _, e := range es {
		if e.Type() == "ai-title" || e.Type() == "cost-state" {
			t.Fatalf("%s should be dropped", e.Type())
		}
	}
}

func TestGraftRefusesPartialTranscript(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	good, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// append a truncated line, as a transcript being written right now has
	if err := os.WriteFile(src, append(good, []byte(`{"type":"user","uuid":`+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Graft(src, "u3", t.TempDir()); err != ErrPartialTranscript {
		t.Fatalf("got %v want ErrPartialTranscript — a dropped line can break the parent chain", err)
	}
}

func TestGraftDropsSessionScopedBookkeeping(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	good, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Entry types that really occur, each carrying state that belongs to the
	// session being branched FROM.
	extra := strings.Join([]string{
		`{"type":"mode","mode":"bypassPermissions","sessionId":"S"}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"S"}`,
		`{"type":"queue-operation","operation":"add","content":"a queued prompt from the old session","sessionId":"S"}`,
		`{"type":"relocated","relocatedCwd":"/old/worktree","sessionId":"S"}`,
		`{"type":"file-history-snapshot","messageId":"m1","snapshot":{}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(src, append(good, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}

	_, dst, err := Graft(src, "u3", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{
		"bypassPermissions",
		"a queued prompt from the old session",
		"/old/worktree",
		"file-history-snapshot",
	} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("old-session state leaked into the graft: %q", leak)
		}
	}
	es, _, _, err := ParseFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.UUID() == "" && e.Type() != "last-prompt" {
			t.Fatalf("uuid-less %q entry carried into the new session", e.Type())
		}
	}
}

func TestGraftRefusesVersionMismatchAfterTheFirstEntry(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	good, err := os.ReadFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's FIRST entry is 2.1.278, so a first-entry-wins check would
	// accept this file. A transcript spanning an upgrade must be refused.
	later := []byte(`{"type":"user","uuid":"u9","parentUuid":"u3","sessionId":"S","cwd":"/repo","version":"99.0.0","message":{"role":"user","content":[{"type":"text","text":"later"}]}}` + "\n")
	if err := os.WriteFile(src, append(good, later...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Graft(src, "u3", t.TempDir()); err != ErrUnsupportedVersion {
		t.Fatalf("got %v want ErrUnsupportedVersion", err)
	}
}

func TestGraftRefusesUnknownFormatVersion(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	src := filepath.Join(t.TempDir(), "s.jsonl")
	body := `{"type":"user","uuid":"u1","parentUuid":null,"sessionId":"S","cwd":"/x","version":"99.0.0","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Graft(src, "u1", t.TempDir()); err != ErrUnsupportedVersion {
		t.Fatalf("got %v want ErrUnsupportedVersion", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/ -run Graft -v`
Expected: FAIL — `undefined: Graft`.

- [ ] **Step 3: Implement the write**

In `internal/claude/graft.go`, REPLACE the existing single-line
`import "errors"` with this block — do not add a second import declaration,
which would import `errors` twice and fail to compile:

```go
import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)
```

Then append the rest to the same file:

```go

// ErrUnsupportedVersion means the transcript was written by a Claude Code
// whose format this adapter has not been verified against. Refusing is
// correct: a wrong graft produces a plausible session with wrong history.
var ErrUnsupportedVersion = errors.New("unsupported Claude Code transcript version")

// ErrPartialTranscript means some lines of the source transcript could not be
// parsed, so its parentUuid chain cannot be trusted.
var ErrPartialTranscript = errors.New("source transcript has unparseable lines")

// verifiedMajorMinor is the format this adapter was validated against.
const verifiedMajorMinor = "2.1"

func checkVersion(es []Entry) error {
	for _, e := range es {
		v := e.Version()
		if v == "" {
			continue
		}
		parts := strings.SplitN(v, ".", 3)
		if len(parts) < 2 || parts[0]+"."+parts[1] != verifiedMajorMinor {
			return ErrUnsupportedVersion
		}
		// Keep scanning. A transcript can span a Claude Code upgrade, and
		// returning on the first versioned entry would accept a file whose
		// later entries use a format this adapter has never been validated
		// against.
	}
	return nil // no version stamped anywhere: nothing to disagree with
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Graft writes a new transcript containing only the ancestor chain of
// atNode, as a fresh session rooted at dstCWD, and returns its id and path.
// The source transcript is never modified.
func Graft(srcPath, atNode, dstCWD string) (newSessionID, dstPath string, err error) {
	es, skipped, _, err := ParseFile(srcPath)
	if err != nil {
		return "", "", err
	}
	if skipped > 0 {
		// A dropped line can break the parentUuid chain, which would produce
		// a graft that looks fine and carries the wrong history.
		return "", "", ErrPartialTranscript
	}
	if err := checkVersion(es); err != nil {
		return "", "", err
	}
	keep, err := Select(es, atNode)
	if err != nil {
		return "", "", err
	}

	newSessionID, err = newUUIDv4()
	if err != nil {
		return "", "", err
	}

	dstDir := filepath.Join(ProjectsDir(), SlugFor(dstCWD))
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return "", "", err
	}
	dstPath = filepath.Join(dstDir, newSessionID+".jsonl")

	var buf []byte
	for _, e := range es {
		u := e.UUID()
		if u == "" {
			// Every uuid-less entry is session-scoped bookkeeping: mode,
			// permission-mode, atis-latch, queue-operation (which carries
			// queued prompt TEXT), relocated and worktree-state (the old
			// working directory), file-history-snapshot/delta, the artifact
			// ledgers (which carry an accountUuid), last-prompt, ai-title,
			// cost-state. All of it belongs to the session being branched
			// FROM. A denylist here is default-allow and silently leaks
			// whatever entry types Claude Code adds next, so drop the lot.
			// Verified empirically: a graft containing no bookkeeping at all
			// resumes correctly, and Claude Code writes fresh entries of its
			// own on resume.
			continue
		}
		if !keep[u] {
			continue
		}
		// Shallow copy, so the source entries stay untouched. Only top-level
		// keys are rewritten below; nested maps (message, attachment,
		// toolUseResult) still alias the source, so never mutate inside them.
		m := make(map[string]any, len(e.Raw))
		for k, v := range e.Raw {
			m[k] = v
		}
		for _, k := range []string{"sessionId", "session_id"} {
			if _, ok := m[k]; ok {
				m[k] = newSessionID
			}
		}
		if _, ok := m["cwd"]; ok {
			m["cwd"] = dstCWD
		}
		b, err := Marshal(Entry{Raw: m})
		if err != nil {
			return "", "", err
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}

	// The leaf pointer Claude Code writes. Proven not sufficient on its own
	// to truncate history — the pruning above is what does that — but it is
	// what the format contains, so write it.
	leaf, err := Marshal(Entry{Raw: map[string]any{
		"type": "last-prompt", "leafUuid": atNode, "sessionId": newSessionID,
	}})
	if err != nil {
		return "", "", err
	}
	buf = append(buf, leaf...)
	buf = append(buf, '\n')

	tmp, err := os.CreateTemp(dstDir, ".graft-*")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", "", err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return "", "", err
	}
	return newSessionID, dstPath, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: graft write, 0600, tmp-then-rename

Explicit 0600: a graft left to umask downgrades conversation content to
world-readable. Refuses to graft on an unverified format version.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Store

**Files:**
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.Store` with `Version int`, `RepoRoot string`, `Branches map[string]Branch`, `Labels map[string]string`; `store.LabelKey`, `(*Store).SetLabel`; `store.Branch{GraftedFrom From, Title string, CreatedAt time.Time, Artifacts []string}`; `store.From{SessionID, Node string}`; `store.Dir() string`; `store.Load(repoRoot string) (*Store, error)`; `(*Store).Save() error`; `(*Store).Add(sessionID string, b Branch)`.

- [ ] **Step 1: Write the failing test**

Create `internal/store/store_test.go`:

```go
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 || s.RepoRoot != "/repo" || len(s.Branches) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s, _ := Load("/repo")
	s.Add("child-sid", Branch{
		GraftedFrom: From{SessionID: "parent-sid", Node: "u3"},
		Title:       "Session-based auth",
		CreatedAt:   time.Date(2026, 9, 21, 17, 40, 0, 0, time.UTC),
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	b, ok := again.Branches["child-sid"]
	if !ok {
		t.Fatal("branch not persisted")
	}
	if b.GraftedFrom.Node != "u3" || b.GraftedFrom.SessionID != "parent-sid" {
		t.Fatalf("got %+v", b)
	}
	if b.Artifacts == nil {
		t.Fatal("artifacts must serialize as [] not null")
	}
}

func TestDifferentReposDoNotShareAFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	a, _ := Load("/repo/a")
	a.Add("s1", Branch{})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := Load("/repo/b")
	if len(b.Branches) != 0 {
		t.Fatal("repos share state")
	}
}

func TestSaveRefusesAStoreThatDidNotComeFromLoad(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	s := &Store{Version: 1, RepoRoot: "/repo", Branches: map[string]Branch{}}
	if err := s.Save(); err != ErrNoPath {
		t.Fatalf("got %v want ErrNoPath — otherwise Save writes tree.json into the process cwd", err)
	}
	if _, err := os.Stat("tree.json"); err == nil {
		os.Remove("tree.json")
		t.Fatal("Save wrote tree.json into the working directory")
	}
}

func TestConcurrentSavesKeepBothBranches(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	// Two panes each Load the same store...
	paneA, _ := Load("/repo")
	paneB, _ := Load("/repo")

	// ...each branches from a different turn...
	paneA.Add("session-a", Branch{GraftedFrom: From{SessionID: "src", Node: "u1"}})
	paneB.Add("session-b", Branch{GraftedFrom: From{SessionID: "src", Node: "u3"}})

	// ...and both save.
	if err := paneA.Save(); err != nil {
		t.Fatal(err)
	}
	if err := paneB.Save(); err != nil {
		t.Fatal(err)
	}

	final, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := final.Branches["session-a"]; !ok {
		t.Fatal("pane A's branch was silently discarded by pane B's save")
	}
	if _, ok := final.Branches["session-b"]; !ok {
		t.Fatal("pane B's branch is missing")
	}
}

func TestSuccessiveCorruptionsAreBothPreserved(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	s, _ := Load("/repo")
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(s.path, []byte("{{{ not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load("/repo"); err != nil {
			t.Fatal(err)
		}
	}
	m, err := filepath.Glob(filepath.Join(dir, "tree.json.corrupt.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d corrupt backups want 2 — a second corruption must not overwrite the first", len(m))
	}
}

func TestCorruptStoreIsBackedUpNotFatal(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	s, _ := Load("/repo")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("{{{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	again, err := Load("/repo")
	if err != nil {
		t.Fatalf("corrupt store must not be fatal: %v", err)
	}
	if len(again.Branches) != 0 {
		t.Fatal("want empty store")
	}
	m, err := filepath.Glob(s.path + ".corrupt.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatal("corrupt file was not preserved as a backup")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -v`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Implement the store**

Create `internal/store/store.go`:

```go
// Package store persists the only data herdr-tree owns: the edge recording
// that one session was branched from a turn of another. Everything else in
// the tree is derived from transcripts and never duplicated here.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type From struct {
	SessionID string `json:"session_id"`
	Node      string `json:"node"`
}

type Branch struct {
	GraftedFrom From      `json:"grafted_from"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	Artifacts   []string  `json:"artifacts"` // always [], populated in v1.1
}

type Store struct {
	Version  int               `json:"version"`
	RepoRoot string            `json:"repo_root"`
	Branches map[string]Branch `json:"branches"`
	// Labels marks turns the user wants to find again, keyed "<session>:<turn>".
	// Landmarking a turn is deliberately separate from branching from it: in
	// practice you notice a point matters before you know whether you will go
	// back to it, and a label costs nothing while a branch costs a session.
	Labels map[string]string `json:"labels,omitempty"`

	path string
}

// Dir is Herdr's per-plugin config directory.
func Dir() string {
	if d := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); d != "" {
		return d
	}
	out, err := exec.Command("herdr", "plugin", "config-dir", "herdr-tree").Output()
	if err == nil {
		// Only accept something that looks like a path. A zero exit with a
		// warning or a diagnostic on stdout must fall through to the default,
		// not become the config directory.
		if d := strings.TrimSpace(string(out)); filepath.IsAbs(d) {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "plugins", "config", "herdr-tree")
}

func pathFor(repoRoot string) string {
	sum := sha256.Sum256([]byte(repoRoot))
	return filepath.Join(Dir(), hex.EncodeToString(sum[:])[:12], "tree.json")
}

// Load reads the store for a repo. A missing store is an empty store. A
// corrupt store is renamed aside and reported as empty: only graft edges
// are lost, and transcripts and artifacts are untouched.
func Load(repoRoot string) (*Store, error) {
	p := pathFor(repoRoot)
	s := &Store{Version: 1, RepoRoot: repoRoot, Branches: map[string]Branch{}, path: p}

	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var loaded Store
	if err := json.Unmarshal(b, &loaded); err != nil {
		// Timestamped so a second corruption does not overwrite the first.
		// A failed rename is deliberately ignored: returning a working empty
		// store matters more than preserving the backup, and the unreadable
		// file is left in place for the user to inspect.
		_ = os.Rename(p, fmt.Sprintf("%s.corrupt.%d", p, time.Now().UnixNano()))
		return s, nil
	}
	if loaded.Branches == nil {
		loaded.Branches = map[string]Branch{}
	}
	loaded.path = p
	loaded.RepoRoot = repoRoot
	if loaded.Version == 0 {
		loaded.Version = 1
	}
	return &loaded, nil
}

// LabelKey identifies a turn for labelling.
func LabelKey(sessionID, turnID string) string { return sessionID + ":" + turnID }

// SetLabel records or clears a landmark on a turn. An empty text removes it,
// so the same key toggles.
func (s *Store) SetLabel(sessionID, turnID, text string) {
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	k := LabelKey(sessionID, turnID)
	if text == "" {
		delete(s.Labels, k)
		return
	}
	s.Labels[k] = text
}

// Add records a graft edge, keyed by the new session's id.
func (s *Store) Add(sessionID string, b Branch) {
	if b.Artifacts == nil {
		b.Artifacts = []string{}
	}
	s.Branches[sessionID] = b
}

// ErrNoPath means Save was called on a Store that did not come from Load, so
// it has no file to write to. Without this guard filepath.Dir("") is ".", and
// Save would silently create tree.json in the process's working directory.
var ErrNoPath = errors.New("store has no path; use Load to obtain one")

// Save merges this store's branches into whatever is on disk now, then writes
// atomically.
//
// The merge matters: two Herdr panes can each Load, each Add a DIFFERENT
// branch, and each Save. A plain overwrite would silently discard the branch
// the other pane just created — and a graft edge is the one piece of data
// that exists nowhere else, so losing it orphans a real session in the tree.
// Re-reading first costs one file read and removes the whole race. v1 never
// deletes a branch, so a merge can never resurrect something intentionally
// removed.
func (s *Store) Save() error {
	if s.path == "" {
		return ErrNoPath
	}
	if onDisk, err := Load(s.RepoRoot); err == nil {
		for id, b := range onDisk.Branches {
			if _, ours := s.Branches[id]; !ours {
				s.Branches[id] = b
			}
		}
		for k, v := range onDisk.Labels {
			if s.Labels == nil {
				s.Labels = map[string]string{}
			}
			if _, ours := s.Labels[k]; !ours {
				s.Labels[k] = v
			}
		}
	}
	for id, b := range s.Branches {
		if b.Artifacts == nil {
			b.Artifacts = []string{}
			s.Branches[id] = b
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tree-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "$(cat <<'EOF'
feat: graft-edge store in Herdr's plugin config dir

Stores only the edge that exists nowhere else. A corrupt store is preserved
aside and degraded to empty rather than being fatal.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Forest assembly

**Files:**
- Create: `internal/tree/tree.go`
- Test: `internal/tree/tree_test.go`

**Interfaces:**
- Consumes: `adapter.Session`, `adapter.Node` (Task 1); `store.Store` (Task 7).
- Produces: `tree.Node{Node adapter.Node, SessionID string, SessionCWD string, SessionTitle string, IsSessionRoot bool, Grafted bool, Broken bool, Children []*tree.Node}`; `tree.Build(sessions []adapter.Session, s *store.Store) []*tree.Node`.

A session is a linear chain. A graft edge attaches one session's first turn as a child of a turn in another session. Sessions with no graft parent are roots.

- [ ] **Step 1: Write the failing test**

Create `internal/tree/tree_test.go`:

```go
package tree

import (
	"strings"
	"testing"
	"time"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
)

func sess(id string, nodes ...string) adapter.Session {
	s := adapter.Session{ID: id, Title: "t-" + id, Updated: time.Now()}
	for _, n := range nodes {
		s.Nodes = append(s.Nodes, adapter.Node{ID: n, Title: "turn " + n})
	}
	return s
}

func TestLinearSessionBecomesAChain(t *testing.T) {
	roots := Build([]adapter.Session{sess("s1", "n1", "n2", "n3")}, emptyStore())
	if len(roots) != 1 {
		t.Fatalf("roots %d", len(roots))
	}
	n := roots[0]
	if n.Node.ID != "n1" || !n.IsSessionRoot {
		t.Fatalf("root %+v", n)
	}
	if len(n.Children) != 1 || n.Children[0].Node.ID != "n2" {
		t.Fatal("n2 should hang off n1")
	}
	if len(n.Children[0].Children) != 1 || n.Children[0].Children[0].Node.ID != "n3" {
		t.Fatal("n3 should hang off n2")
	}
}

func TestGraftEdgeNestsChildSessionUnderParentTurn(t *testing.T) {
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n2"}})

	roots := Build([]adapter.Session{sess("s1", "n1", "n2", "n3"), sess("s2", "m1", "m2")}, st)
	if len(roots) != 1 {
		t.Fatalf("grafted session must not be a root: %d roots", len(roots))
	}
	n2 := roots[0].Children[0]
	if n2.Node.ID != "n2" {
		t.Fatalf("expected n2, got %s", n2.Node.ID)
	}
	var ids []string
	for _, c := range n2.Children {
		ids = append(ids, c.Node.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("n2 children %v want n3 and m1", ids)
	}
	var grafted *Node
	for _, c := range n2.Children {
		if c.Grafted {
			grafted = c
		}
	}
	if grafted == nil || grafted.Node.ID != "m1" {
		t.Fatalf("m1 should be the grafted child, got %+v", grafted)
	}
	if !grafted.IsSessionRoot || grafted.SessionID != "s2" {
		t.Fatalf("grafted child metadata wrong: %+v", grafted)
	}
}

func TestDanglingGraftParentFallsBackToRoot(t *testing.T) {
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "gone", Node: "nope"}})
	roots := Build([]adapter.Session{sess("s2", "m1")}, st)
	if len(roots) != 1 || roots[0].Node.ID != "m1" {
		t.Fatalf("a session whose parent vanished must still render: %+v", roots)
	}
}

func TestBuildCarriesTheSessionPath(t *testing.T) {
	// Without this the TUI rebuilds a Session with no Path, and Preview and
	// Branch silently fall back to reconstructing it — wrong for any session
	// that relocated into a worktree.
	s := sess("s1", "n1", "n2")
	s.Path = "/somewhere/-odd-project-dir/s1.jsonl"
	roots := Build([]adapter.Session{s}, emptyStore())
	if roots[0].SessionPath != s.Path {
		t.Fatalf("root SessionPath = %q want %q", roots[0].SessionPath, s.Path)
	}
	if roots[0].Children[0].SessionPath != s.Path {
		t.Fatalf("child SessionPath = %q want %q", roots[0].Children[0].SessionPath, s.Path)
	}
}

func TestGraftedSiblingsRenderInAStableOrder(t *testing.T) {
	// Two branches taken from the SAME turn. Map iteration order is randomised
	// per run, so without explicit ordering these two swap places between
	// launches of a tree the user navigates by position.
	sessions := []adapter.Session{
		sess("s1", "n1", "n2"),
		sess("first", "a1"),
		sess("second", "b1"),
	}
	var seen []string
	for i := 0; i < 20; i++ {
		st := emptyStore()
		st.Add("first", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})
		st.Add("second", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})

		roots := Build(sessions, st)
		var order []string
		for _, c := range roots[0].Children {
			order = append(order, c.SessionID)
		}
		got := strings.Join(order, ",")
		if i == 0 {
			seen = order
			continue
		}
		if got != strings.Join(seen, ",") {
			t.Fatalf("grafted sibling order changed between runs: %v then %v", seen, order)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("want n2 plus both grafted children under n1, got %v", seen)
	}
}

func TestGraftParentTurnGoneFallsBackToRoot(t *testing.T) {
	// The parent session is present and readable, but the specific turn the
	// branch was taken from is no longer there.
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "vanished"}})

	roots := Build([]adapter.Session{sess("s1", "n1", "n2"), sess("s2", "m1")}, st)
	if len(roots) != 2 {
		t.Fatalf("want both sessions as roots, got %d: %+v", len(roots), roots)
	}
	var sawChild bool
	for _, r := range roots {
		if r.SessionID == "s2" && r.Node.ID == "m1" {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatal("a branch whose graft turn vanished must still render as a root, not disappear")
	}
}

func TestGraftFromABrokenParentFallsBackToRoot(t *testing.T) {
	// A parent whose transcript became unreadable has zero turns, so its node
	// index is empty and the child's graft point cannot be found.
	st := emptyStore()
	st.Add("s2", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})

	broken := adapter.Session{ID: "s1", Title: "t-s1", Broken: true}
	roots := Build([]adapter.Session{broken, sess("s2", "m1")}, st)
	if len(roots) != 2 {
		t.Fatalf("want 2 roots (the broken parent and the orphaned child), got %d: %+v", len(roots), roots)
	}
}

func TestGraftEdgeForAMissingSessionIsIgnored(t *testing.T) {
	// The store remembers a branch whose transcript the user has since deleted.
	st := emptyStore()
	st.Add("deleted-session", store.Branch{GraftedFrom: store.From{SessionID: "s1", Node: "n1"}})

	roots := Build([]adapter.Session{sess("s1", "n1", "n2")}, st)
	if len(roots) != 1 || roots[0].SessionID != "s1" {
		t.Fatalf("a stale edge must not invent a node: %+v", roots)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].Node.ID != "n2" {
		t.Fatalf("the surviving session must be unaffected: %+v", roots[0].Children)
	}
}

func TestEmptySessionStillRenders(t *testing.T) {
	roots := Build([]adapter.Session{sess("s1")}, emptyStore())
	if len(roots) != 1 {
		t.Fatalf("roots %d want 1", len(roots))
	}
	if !roots[0].Broken {
		t.Fatalf("a session with no turns must be marked Broken so it renders as a warning row: %+v", roots[0])
	}
	if !roots[0].IsSessionRoot || roots[0].SessionID != "s1" {
		t.Fatalf("got %+v", roots[0])
	}
}

func emptyStore() *store.Store {
	return &store.Store{Version: 1, Branches: map[string]store.Branch{}}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tree/ -v`
Expected: FAIL — `undefined: Build`.

- [ ] **Step 3: Implement forest assembly**

Create `internal/tree/tree.go`:

```go
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
				SessionID: sess.ID, SessionCWD: sess.CWD, SessionPath: sess.Path,
				SessionTitle: sess.Title, IsSessionRoot: true, Broken: true,
			}
			chains[sess.ID] = n
			continue
		}
		var head, prev *Node
		for i, t := range sess.Nodes {
			n := &Node{
				Node: t, SessionID: sess.ID, SessionCWD: sess.CWD,
				SessionPath: sess.Path, SessionTitle: sess.Title,
				IsSessionRoot: i == 0, Broken: sess.Broken,
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tree/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/tree/
git commit -m "$(cat <<'EOF'
feat: forest assembly from sessions and graft edges

A dangling graft parent degrades the child to a root rather than dropping it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Current session detection

**Files:**
- Create: `internal/claude/current.go`
- Test: `internal/claude/current_test.go`

**Interfaces:**
- Consumes: `adapter.Pane` (Task 1).
- Produces: `claude.SessionsDir() string`; `claude.Current(p adapter.Pane) (string, error)`; `claude.ErrAmbiguousSession`.

Herdr's `pane.agent_session.value` is the last Claude session id *observed* in the pane, which a headless run silently overwrites — observed live during the spike, pointing at a deleted session. `~/.claude/sessions/<pid>.json` carries `kind: "interactive"` and is authoritative. Disagreement resolves to unknown, never to a guess: branching from the wrong session destroys work silently.

- [ ] **Step 1: Write the failing test**

Create `internal/claude/current_test.go`:

```go
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"herdr-tree/internal/adapter"
)

func writeReg(t *testing.T, dir, pid, sid, cwd, kind string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"pid": pid, "sessionId": sid, "cwd": cwd, "kind": kind,
	})
	if err := os.WriteFile(filepath.Join(dir, pid+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentPrefersInteractiveRegistryEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "real-sid", "/repo", "interactive")

	got, err := Current(adapter.Pane{CWD: "/repo", AgentSessionID: "stale-sid"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "real-sid" {
		t.Fatalf("got %q; Herdr's stale value must not win", got)
	}
}

func TestCurrentIgnoresOtherCwds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "other-sid", "/elsewhere", "interactive")

	if _, err := Current(adapter.Pane{CWD: "/repo"}); err == nil {
		t.Fatal("want an error when no session matches the pane")
	}
}

func TestCurrentIgnoresHeadlessSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "headless-sid", "/repo", "print")

	if _, err := Current(adapter.Pane{CWD: "/repo"}); err == nil {
		t.Fatal("a headless session is not the pane's session")
	}
}

func TestAmbiguityResolvedByHerdrHint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "sid-a", "/repo", "interactive")
	writeReg(t, dir, "2", "sid-b", "/repo", "interactive")

	got, err := Current(adapter.Pane{CWD: "/repo", AgentSessionID: "sid-b"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "sid-b" {
		t.Fatalf("got %q want sid-b", got)
	}
}

func TestCurrentRefusesAPaneWithNoCWD(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	// Exactly one session running anywhere: the tempting case to "just pick it".
	writeReg(t, dir, "1", "some-sid", "/somewhere/else", "interactive")

	if _, err := Current(adapter.Pane{CWD: ""}); err != ErrUnknownCWD {
		t.Fatalf("got %v want ErrUnknownCWD — an unknown pane directory must not match every session", err)
	}
}

func TestCurrentMatchesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)

	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Claude recorded the real path; Herdr reports the pane through the link.
	writeReg(t, dir, "1", "the-sid", real, "interactive")

	got, err := Current(adapter.Pane{CWD: link})
	if err != nil {
		t.Fatalf("symlinked pane cwd should still match: %v", err)
	}
	if got != "the-sid" {
		t.Fatalf("got %q want the-sid", got)
	}
}

func TestCurrentToleratesATrailingSlash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	repoDir := t.TempDir()
	writeReg(t, dir, "1", "the-sid", repoDir, "interactive")

	got, err := Current(adapter.Pane{CWD: repoDir + "/"})
	if err != nil {
		t.Fatalf("trailing slash should not break the match: %v", err)
	}
	if got != "the-sid" {
		t.Fatalf("got %q want the-sid", got)
	}
}

func TestAmbiguityWithoutUsableHintIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_DIR", dir)
	writeReg(t, dir, "1", "sid-a", "/repo", "interactive")
	writeReg(t, dir, "2", "sid-b", "/repo", "interactive")

	if _, err := Current(adapter.Pane{CWD: "/repo", AgentSessionID: "sid-gone"}); err != ErrAmbiguousSession {
		t.Fatalf("got %v want ErrAmbiguousSession", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/ -run Current -v`
Expected: FAIL — `undefined: Current`.

- [ ] **Step 3: Implement current-session detection**

Create `internal/claude/current.go`:

```go
package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"herdr-tree/internal/adapter"
)

var (
	ErrNoSession        = errors.New("no interactive Claude session for this pane")
	ErrAmbiguousSession = errors.New("several interactive Claude sessions match this pane")
	ErrUnknownCWD       = errors.New("pane reported no working directory")
)

// SessionsDir is Claude Code's live process registry.
func SessionsDir() string {
	if d := os.Getenv("CLAUDE_SESSIONS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

type regEntry struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
}

// samePath compares two directories by identity rather than by spelling.
// Herdr and Claude Code can report the same directory differently — one
// through a symlink, one with a trailing slash — and a plain string compare
// would drop a real match to zero and report no session at all.
func samePath(a, b string) bool { return normPath(a) == normPath(b) }

func normPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p // may not exist any more; the cleaned spelling is the best we have
}

// Current resolves the pane's Claude session. The registry is authoritative;
// Herdr's agent_session value is only a tie-breaker, because it records the
// last session id seen in the pane including headless ones.
func Current(p adapter.Pane) (string, error) {
	if p.CWD == "" {
		// Without a pane directory there is nothing to match against, and
		// matching everything would silently return whichever session happens
		// to be the only one running. Missing evidence is not evidence that
		// any session will do.
		return "", ErrUnknownCWD
	}
	paths, err := filepath.Glob(filepath.Join(SessionsDir(), "*.json"))
	if err != nil {
		return "", err
	}
	var matches []string
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e regEntry
		if json.Unmarshal(b, &e) != nil {
			continue
		}
		if e.Kind != "interactive" || e.SessionID == "" {
			continue
		}
		if !samePath(e.CWD, p.CWD) {
			continue
		}
		matches = append(matches, e.SessionID)
	}

	switch len(matches) {
	case 0:
		return "", ErrNoSession
	case 1:
		return matches[0], nil
	}
	for _, m := range matches {
		if m == p.AgentSessionID {
			return m, nil
		}
	}
	return "", ErrAmbiguousSession
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: current-session detection from Claude's process registry

Herdr's pane agent_session is a hint only; it was observed pointing at a
deleted session. Ambiguity resolves to an error, never a guess.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Herdr CLI wrapper

**Files:**
- Create: `internal/herdr/herdr.go`
- Test: `internal/herdr/herdr_test.go`

**Interfaces:**
- Consumes: `adapter.Pane` (Task 1).
- Produces: `herdr.Bin() string`; `herdr.PaneCurrent() (adapter.Pane, error)` populating `Agent` and `AgentSessionID`; `herdr.Split(cwd string) (paneID string, err error)`; `herdr.AgentStart(name, paneID, sessionID string) error`; `herdr.OpenTreePane(cwd string) error`; `herdr.parsePaneCurrent([]byte) (adapter.Pane, error)`; `herdr.parseSplit([]byte) (string, error)`.

Herdr owns process and pane lifecycle. This package shells out and parses JSON; the parsers are what get tested, so no test needs a running Herdr.

- [ ] **Step 1: Write the failing test**

Create `internal/herdr/herdr_test.go`:

```go
package herdr

import (
	"errors"
	"strings"
	"testing"
)

const paneCurrentJSON = `{"id":"cli:pane:current","result":{"pane":{
"agent":"claude",
"agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"6d6dffb3-1677-4215-888a-819585242bac"},
"cwd":"/home/somliga/projects/herdr-tree",
"pane_id":"wA:p1","tab_id":"wA:t1","workspace_id":"wA"},"type":"pane_current"}}`

const splitJSON = `{"id":"cli:pane:split","result":{"pane":{"pane_id":"wA:p2","cwd":"/repo"}}}`

func TestParsePaneCurrent(t *testing.T) {
	p, err := parsePaneCurrent([]byte(paneCurrentJSON))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "wA:p1" {
		t.Fatalf("id %q", p.ID)
	}
	if p.CWD != "/home/somliga/projects/herdr-tree" {
		t.Fatalf("cwd %q", p.CWD)
	}
	if p.AgentSessionID != "6d6dffb3-1677-4215-888a-819585242bac" {
		t.Fatalf("agent session %q", p.AgentSessionID)
	}
	if p.Agent != "claude" {
		t.Fatalf("agent %q", p.Agent)
	}
}

func TestParsePaneCurrentWithoutAgent(t *testing.T) {
	p, err := parsePaneCurrent([]byte(`{"result":{"pane":{"pane_id":"wA:p1","cwd":"/x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.AgentSessionID != "" {
		t.Fatalf("want empty agent session, got %q", p.AgentSessionID)
	}
}

func TestParseSplit(t *testing.T) {
	id, err := parseSplit([]byte(splitJSON))
	if err != nil {
		t.Fatal(err)
	}
	if id != "wA:p2" {
		t.Fatalf("got %q", id)
	}
}

func TestParseSplitMissingPane(t *testing.T) {
	if _, err := parseSplit([]byte(`{"result":{}}`)); err == nil {
		t.Fatal("want an error when no pane id is returned")
	}
}

func TestArgvOrderingAndSeparator(t *testing.T) {
	// herdr does NOT accept --flag=value (verified against the real binary:
	// it answers "unknown option"), so ordering and the -- separator are the
	// only things standing between data and the flag parser.
	got := strings.Join(splitArgv("/repo"), " ")
	want := "pane split --current --direction right --cwd /repo --no-focus"
	if got != want {
		t.Fatalf("splitArgv:\n got %q\nwant %q", got, want)
	}

	got = strings.Join(agentStartArgv("tree-abc", "wA:p2", "sid-1"), " ")
	want = "agent start tree-abc --kind claude --pane wA:p2 -- --resume sid-1"
	if got != want {
		t.Fatalf("agentStartArgv:\n got %q\nwant %q", got, want)
	}
	// Everything after "--" belongs to claude, not herdr.
	a := agentStartArgv("tree-abc", "wA:p2", "sid-1")
	sep := -1
	for i, v := range a {
		if v == "--" {
			sep = i
		}
	}
	if sep == -1 {
		t.Fatal("no -- separator: claude's flags would be parsed by herdr")
	}
	if a[sep+1] != "--resume" || a[sep+2] != "sid-1" {
		t.Fatalf("resume args are not behind the separator: %v", a)
	}

	got = strings.Join(openTreePaneArgv("/repo"), " ")
	want = "plugin pane open --plugin herdr-tree --entrypoint tree --placement overlay --cwd /repo"
	if got != want {
		t.Fatalf("openTreePaneArgv:\n got %q\nwant %q", got, want)
	}
}

func TestRefusesArgumentsThatWouldBeReadAsFlags(t *testing.T) {
	// A directory literally named "-foo" cannot be passed safely, because the
	// --flag=value escape hatch does not exist in this CLI. Refusing beats
	// letting herdr parse it as a flag.
	if _, err := Split("-rf"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("Split: got %v want ErrUnsafeArgument", err)
	}
	if err := OpenTreePane("--placement"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("OpenTreePane: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("-x", "wA:p1", "sid"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart name: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("tree-a", "-p", "sid"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart pane: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("tree-a", "wA:p1", "-resume"); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart session: got %v want ErrUnsafeArgument", err)
	}
	if err := AgentStart("tree-a", "wA:p1", ""); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("AgentStart empty: got %v want ErrUnsafeArgument", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/herdr/ -v`
Expected: FAIL — `undefined: parsePaneCurrent`.

- [ ] **Step 3: Implement the wrapper**

Create `internal/herdr/herdr.go`:

```go
// Package herdr shells out to the herdr CLI. Herdr owns pane and process
// lifecycle; this plugin only asks it for things.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"herdr-tree/internal/adapter"
)

// timeout bounds every herdr invocation. `agent start` waits for the agent to
// become ready (herdr's own default is 30s), and these calls are made from the
// TUI, so an unbounded wait is a permanently stuck overlay with no way out.
const timeout = 45 * time.Second

// ErrUnsafeArgument means a value would be read as a flag rather than as data.
// herdr does NOT accept the --flag=value form (verified: it answers "unknown
// option"), so a value beginning with "-" cannot be passed safely at all and
// the only correct move is to refuse it.
var ErrUnsafeArgument = errors.New("value would be read as a flag")

func checkArg(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty: %w", what, ErrUnsafeArgument)
	}
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("%s %q: %w", what, v, ErrUnsafeArgument)
	}
	return nil
}

// Bin is the herdr binary. Herdr sets HERDR_BIN_PATH when it invokes a plugin.
func Bin() string {
	if b := os.Getenv("HERDR_BIN_PATH"); b != "" {
		return b
	}
	return "herdr"
}

func run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, Bin(), args...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("herdr %v timed out after %s", args, timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// herdr's own diagnostic, about panes and processes — not
			// conversation content.
			return nil, fmt.Errorf("herdr %v: %s", args, ee.Stderr)
		}
		return nil, err
	}
	return out, nil
}

// The argv builders are separated from the calls so that flag ordering and
// the placement of "--" are regression-tested without a running herdr.

func splitArgv(cwd string) []string {
	return []string{"pane", "split", "--current", "--direction", "right", "--cwd", cwd, "--no-focus"}
}

func agentStartArgv(name, paneID, sessionID string) []string {
	return []string{"agent", "start", name, "--kind", "claude", "--pane", paneID, "--", "--resume", sessionID}
}

func openTreePaneArgv(cwd string) []string {
	return []string{"plugin", "pane", "open",
		"--plugin", "herdr-tree", "--entrypoint", "tree",
		"--placement", "overlay", "--cwd", cwd}
}

type paneCurrentResp struct {
	Result struct {
		Pane struct {
			PaneID       string `json:"pane_id"`
			CWD          string `json:"cwd"`
			Agent        string `json:"agent"`
			AgentSession *struct {
				Value string `json:"value"`
			} `json:"agent_session"`
		} `json:"pane"`
	} `json:"result"`
}

func parsePaneCurrent(b []byte) (adapter.Pane, error) {
	var r paneCurrentResp
	if err := json.Unmarshal(b, &r); err != nil {
		return adapter.Pane{}, err
	}
	p := adapter.Pane{
		ID:    r.Result.Pane.PaneID,
		CWD:   r.Result.Pane.CWD,
		Agent: r.Result.Pane.Agent,
	}
	if r.Result.Pane.AgentSession != nil {
		p.AgentSessionID = r.Result.Pane.AgentSession.Value
	}
	if p.ID == "" {
		return adapter.Pane{}, errors.New("herdr returned no pane")
	}
	return p, nil
}

// PaneCurrent describes the pane this process was invoked from.
func PaneCurrent() (adapter.Pane, error) {
	out, err := run("pane", "current", "--current")
	if err != nil {
		return adapter.Pane{}, err
	}
	return parsePaneCurrent(out)
}

type splitResp struct {
	Result struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	} `json:"result"`
}

func parseSplit(b []byte) (string, error) {
	var r splitResp
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	if r.Result.Pane.PaneID == "" {
		return "", errors.New("herdr returned no pane id")
	}
	return r.Result.Pane.PaneID, nil
}

// Split opens a sibling pane to the right without stealing focus.
func Split(cwd string) (string, error) {
	if err := checkArg("cwd", cwd); err != nil {
		return "", err
	}
	out, err := run(splitArgv(cwd)...)
	if err != nil {
		return "", err
	}
	return parseSplit(out)
}

// AgentStart launches Claude in an existing pane, resuming a session.
func AgentStart(name, paneID, sessionID string) error {
	for _, c := range []struct{ what, v string }{
		{"agent name", name}, {"pane id", paneID}, {"session id", sessionID},
	} {
		if err := checkArg(c.what, c.v); err != nil {
			return err
		}
	}
	_, err := run(agentStartArgv(name, paneID, sessionID)...)
	return err
}

// OpenTreePane asks Herdr to open this plugin's overlay pane.
func OpenTreePane(cwd string) error {
	if err := checkArg("cwd", cwd); err != nil {
		return err
	}
	_, err := run(openTreePaneArgv(cwd)...)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/herdr/ -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/herdr/
git commit -m "$(cat <<'EOF'
feat: herdr CLI wrapper

Herdr owns pane and process lifecycle; parsers are tested without a running
server.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Claude adapter implementation

**Files:**
- Create: `internal/claude/adapter.go`
- Test: `internal/claude/adapter_test.go`

**Interfaces:**
- Consumes: everything in `internal/claude` (Tasks 2-6, 9); `herdr.Split`, `herdr.AgentStart` (Task 10).
- Produces: `claude.New() adapter.Adapter` satisfying the Task 1 interface, including `Preview`.

- [ ] **Step 1: Write the failing test**

Create `internal/claude/adapter_test.go`:

```go
package claude

import (
	"os"
	"path/filepath"
	"testing"

	"herdr-tree/internal/adapter"
)

func TestAdapterSatisfiesInterface(t *testing.T) {
	var _ adapter.Adapter = New()
}

func TestAdapterName(t *testing.T) {
	if New().Name() != "claude" {
		t.Fatalf("got %q", New().Name())
	}
}

func TestAdapterBranchWritesNewSession(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()

	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, err := New().Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions %d", len(sessions))
	}
	src := sessions[0]

	sid, err := New().Branch(src, src.Nodes[1].ID, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(projects, SlugFor(repoDir), sid+".jsonl")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("grafted session not written: %v", err)
	}
}

func TestAdapterPreviewCountsWhatIsCarried(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, _ := New().Discover(repoDir)
	src := sessions[0]
	turns, entries, size, err := New().Preview(src, src.Nodes[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("turns %d want 2", turns)
	}
	if entries != 7 {
		t.Fatalf("entries %d want 7", entries)
	}
	if size <= 0 {
		t.Fatalf("size %d", size)
	}
}

func TestAdapterReadsViaTheDiscoveredPath(t *testing.T) {
	// The transcript sits in a directory whose name does not match the
	// session's cwd, exactly as a relocated session does. Branch and Preview
	// must still find it.
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	id := "33333333-3333-4333-8333-333333333333"

	es, _, err := ParseFile("testdata/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(projects, "-relocated-elsewhere")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(odd, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if _, ok := e.Raw["cwd"]; ok {
			e.Raw["cwd"] = repoDir
		}
		e.Raw["sessionId"] = id
		b, _ := Marshal(e)
		f.Write(append(b, '\n'))
	}
	f.Close()

	sessions, err := New().Discover(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions %d want 1", len(sessions))
	}
	if _, _, _, err := New().Preview(sessions[0], sessions[0].Nodes[1].ID); err != nil {
		t.Fatalf("Preview could not read a relocated session: %v", err)
	}
	if _, err := New().Branch(sessions[0], sessions[0].Nodes[1].ID, repoDir); err != nil {
		t.Fatalf("Branch could not read a relocated session: %v", err)
	}
}

func TestAgentNameIsUniquePerPane(t *testing.T) {
	sid := "60c5b417-ec35-4ea6-93bb-8246b877b19f"
	a := agentName(sid, "wA:p2")
	b := agentName(sid, "wA:p3")
	if a == b {
		t.Fatalf("the same session in two panes produced the same name %q; herdr requires live agent names to be unique", a)
	}
	for _, n := range []string{a, b} {
		if !strings.HasPrefix(n, "tree-") {
			t.Fatalf("name %q must start with tree-", n)
		}
		if len(n) > 32 {
			t.Fatalf("name %q is longer than herdr allows", n)
		}
		for _, r := range n {
			ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
			if !ok {
				t.Fatalf("name %q contains %q, outside [a-z0-9_-]", n, r)
			}
		}
		if n[0] < 'a' || n[0] > 'z' {
			t.Fatalf("name %q must start with a letter", n)
		}
	}
}

func TestPreviewRejectsAnUnknownNode(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, _ := New().Discover(repoDir)
	if _, _, _, err := New().Preview(sessions[0], "no-such-node"); err == nil {
		t.Fatal("want an error for a node that is not in the transcript")
	}
}

func TestAdapterBranchRejectsUnknownNode(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	repoDir := t.TempDir()
	writeSession(t, projects, "11111111-1111-4111-8111-111111111111", repoDir)

	sessions, _ := New().Discover(repoDir)
	if _, err := New().Branch(sessions[0], "no-such-node", repoDir); err == nil {
		t.Fatal("want an error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/ -run Adapter -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement the adapter**

Create `internal/claude/adapter.go`:

```go
package claude

import (
	"fmt"
	"path/filepath"
	"strings"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/herdr"
)

type claudeAdapter struct{}

// New returns the Claude Code adapter.
func New() adapter.Adapter { return claudeAdapter{} }

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Discover(repoRoot string) ([]adapter.Session, error) {
	return Discover(repoRoot)
}

func (claudeAdapter) Current(p adapter.Pane) (string, error) { return Current(p) }

// TranscriptPath is where Claude Code will look for a session started in
// cwd. Use it for a file about to be WRITTEN. To READ an existing session,
// use Session.Path, which is where the file was actually found — the two
// disagree for a session that relocated into a worktree.
func TranscriptPath(sessionID, cwd string) string {
	return filepath.Join(ProjectsDir(), SlugFor(cwd), sessionID+".jsonl")
}

// sourcePath prefers the discovered path and falls back to reconstruction
// for a Session built by hand.
func sourcePath(src adapter.Session) string {
	if src.Path != "" {
		return src.Path
	}
	return TranscriptPath(src.ID, src.CWD)
}

// Preview reports what a graft at atNode would carry, without writing.
func (claudeAdapter) Preview(src adapter.Session, atNode string) (turns, entries int, size int64, err error) {
	es, _, _, err := ParseFile(sourcePath(src))
	if err != nil {
		return 0, 0, 0, err
	}
	keep, err := Select(es, atNode)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range es {
		u := e.UUID()
		if u == "" || !keep[u] {
			continue
		}
		entries++
		if IsPrompt(e) {
			turns++
		}
		if b, merr := Marshal(e); merr == nil {
			size += int64(len(b)) + 1
		}
	}
	return turns, entries, size, nil
}

func (claudeAdapter) Branch(src adapter.Session, atNode, dstCWD string) (string, error) {
	sid, _, err := Graft(sourcePath(src), atNode, dstCWD)
	if err != nil {
		return "", err
	}
	return sid, nil
}

// agentName builds a Herdr agent name for a session in a pane.
//
// It includes the pane because Herdr requires live agent names to be unique,
// and a name derived from the session alone collides the moment the same
// session is opened twice — a retry after a failure, or a session already
// open elsewhere. Resume always creates a fresh pane, so the pane id makes
// the name unique in practice.
//
// Herdr accepts [a-z][a-z0-9_-]{0,31}, so everything is lowercased, anything
// outside that set is dropped, and the result is capped.
func agentName(sessionID, paneID string) string {
	keep := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	n := "tree-" + keep(strings.SplitN(sessionID, "-", 2)[0]) + "-" + keep(paneID)
	if len(n) > 32 {
		n = n[:32]
	}
	return strings.TrimRight(n, "-")
}

// Resume asks Herdr for a pane and starts Claude in it. Nothing is spawned
// by this process.
func (claudeAdapter) Resume(sessionID, cwd string) error {
	paneID, err := herdr.Split(cwd)
	if err != nil {
		return fmt.Errorf("open pane: %w", err)
	}
	if err := herdr.AgentStart(agentName(sessionID, paneID), paneID, sessionID); err != nil {
		// Say that the pane exists, so the empty pane the user is now looking
		// at is explained rather than mysterious.
		return fmt.Errorf("opened pane %s but could not start claude in it: %w", paneID, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -v`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "$(cat <<'EOF'
feat: Claude adapter implementing the neutral interface

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: TUI view model — flatten, fold, navigate

**Files:**
- Create: `internal/tui/model.go`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `tree.Node` (Task 8).
- Produces: `tui.Row{Node *tree.Node, Depth int, HasChildren bool, Folded bool}`; `tui.Model` with `Roots []*tree.Node`, `Cursor int`, `Folded map[*tree.Node]bool`; `(*Model).Rows() []Row`; `(*Model).Down()`, `(*Model).Up()`, `(*Model).Fold()`, `(*Model).Unfold()`, `(*Model).Selected() *tree.Node`.

Navigation and folding are pure functions over the model, so they are tested without rendering anything.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/model_test.go`:

```go
package tui

import (
	"testing"
	"time"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

func chain(ids ...string) *tree.Node {
	var head, prev *tree.Node
	for i, id := range ids {
		n := &tree.Node{
			Node:          adapter.Node{ID: id, Title: "turn " + id},
			SessionID:     "s1",
			IsSessionRoot: i == 0,
		}
		if prev == nil {
			head = n
		} else {
			prev.Children = append(prev.Children, n)
		}
		prev = n
	}
	return head
}

func ids(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Node.Node.ID)
	}
	return out
}

func TestRowsFlattensDepthFirst(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2", "n3")})
	got := ids(m.Rows())
	want := []string{"n1", "n2", "n3"}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
	if m.Rows()[2].Depth != 2 {
		t.Fatalf("depth %d want 2", m.Rows()[2].Depth)
	}
}

func TestFoldHidesDescendants(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2", "n3")})
	m.Fold() // cursor on n1
	got := ids(m.Rows())
	if len(got) != 1 || got[0] != "n1" {
		t.Fatalf("got %v want [n1]", got)
	}
	if !m.Rows()[0].Folded {
		t.Fatal("row should report folded")
	}
	m.Unfold()
	if len(m.Rows()) != 3 {
		t.Fatalf("unfold failed: %v", ids(m.Rows()))
	}
}

func TestCursorClampsAtEnds(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	m.Up()
	if m.Cursor != 0 {
		t.Fatalf("cursor %d want 0", m.Cursor)
	}
	m.Down()
	m.Down()
	m.Down()
	if m.Cursor != 1 {
		t.Fatalf("cursor %d want 1", m.Cursor)
	}
}

func TestFoldOnLeafMovesToParent(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	m.Down() // on n2, a leaf
	m.Fold()
	if m.Cursor != 0 {
		t.Fatalf("folding a leaf should move to its parent; cursor %d", m.Cursor)
	}
}

func TestCyclicTreeDoesNotCrashOrHang(t *testing.T) {
	// A hand-edited or corrupted tree.json can express mutually-nesting graft
	// edges. New()'s recursive walk would overflow the stack — a fatal error
	// that kills the process — and Rows() would loop forever.
	a := &tree.Node{Node: adapter.Node{ID: "a", Title: "A"}, SessionID: "s1"}
	b := &tree.Node{Node: adapter.Node{ID: "b", Title: "B"}, SessionID: "s2"}
	a.Children = append(a.Children, b)
	b.Children = append(b.Children, a)

	done := make(chan int, 1)
	go func() { done <- len(New([]*tree.Node{a}).Rows()) }()
	select {
	case n := <-done:
		if n == 0 {
			t.Fatal("want at least the reachable nodes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cyclic tree hangs: the overlay would freeze with no error")
	}
}

func TestSelectedTracksCursor(t *testing.T) {
	m := New([]*tree.Node{chain("n1", "n2")})
	m.Down()
	if m.Selected().Node.ID != "n2" {
		t.Fatalf("got %q", m.Selected().Node.ID)
	}
}

func TestEmptyForestHasNoSelection(t *testing.T) {
	m := New(nil)
	if len(m.Rows()) != 0 {
		t.Fatal("want no rows")
	}
	if m.Selected() != nil {
		t.Fatal("want nil selection")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement the view model**

Create `internal/tui/model.go`:

```go
// Package tui renders the conversation forest. The navigation model is kept
// separate from rendering so it can be tested as plain functions.
package tui

import "herdr-tree/internal/tree"

type Row struct {
	Node        *tree.Node
	Depth       int
	HasChildren bool
	Folded      bool
}

// Density is how much of the tree is shown. Cycling it is the answer to a
// repo with hundreds of sessions: folding hides a subtree you chose, density
// hides a KIND of row everywhere at once. Borrowed from Pi, whose tree view
// cycles the same way and which is where this plugin's model came from.
type Density int

const (
	DensityAll      Density = iota // every turn
	DensityLabelled                // only labelled turns, plus session roots
	DensityRoots                   // one row per session
)

func (d Density) String() string {
	switch d {
	case DensityLabelled:
		return "labelled"
	case DensityRoots:
		return "sessions"
	default:
		return "all"
	}
}

type Model struct {
	Roots   []*tree.Node
	Cursor  int
	Folded  map[*tree.Node]bool
	Density Density

	parent map[*tree.Node]*tree.Node
}

// CycleDensity advances to the next density, keeping the selected node visible
// where it still can be. A view control that loses your place is worse than no
// view control.
func (m *Model) CycleDensity() {
	was := m.Selected()
	m.Density = (m.Density + 1) % 3
	if was == nil {
		return
	}
	for i, r := range m.Rows() {
		if r.Node == was {
			m.Cursor = i
			return
		}
	}
	// the selected row is hidden at this density: fall back to its nearest
	// visible ancestor rather than jumping to an unrelated row.
	for n := m.parent[was]; n != nil; n = m.parent[n] {
		for i, r := range m.Rows() {
			if r.Node == n {
				m.Cursor = i
				return
			}
		}
	}
	m.Cursor = 0
	m.clamp()
}

// visible reports whether a node is shown at the current density. A session
// root is always shown — hiding one would lose the session entirely, which is
// the failure this whole design exists to avoid.
func (m *Model) visible(n *tree.Node) bool {
	if n.IsSessionRoot {
		return true
	}
	switch m.Density {
	case DensityRoots:
		return false
	case DensityLabelled:
		return n.Label != ""
	default:
		return true
	}
}

// New indexes each node's parent so Fold can jump upward.
//
// The "already seen" check is the same cycle defence as Rows(), and it is
// needed here for a harsher reason: an unguarded recursive walk over a cyclic
// tree overflows the stack, and a Go stack overflow is a fatal error that no
// recover can catch. That kills the plugin process outright rather than
// merely freezing the view. Graft edges live in a plain JSON file that can be
// hand-edited or corrupted into a cycle, so this is reachable.
func New(roots []*tree.Node) *Model {
	m := &Model{Roots: roots, Folded: map[*tree.Node]bool{}, parent: map[*tree.Node]*tree.Node{}}
	var walk func(n *tree.Node)
	walk = func(n *tree.Node) {
		for _, c := range n.Children {
			if _, seen := m.parent[c]; seen {
				continue
			}
			m.parent[c] = n
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return m
}

// Rows flattens the visible forest depth-first.
//
// The visited guard is not theatre: graft edges live in a plain JSON file the
// user can hand-edit, and a cyclic pair of edges would make this walk run
// forever — a frozen overlay with no error. Build itself cannot hang (it is a
// flat pass), so this is the only place the guard is needed. Truncating a
// corrupt tree beats hanging on one.
func (m *Model) Rows() []Row {
	var out []Row
	visited := map[*tree.Node]bool{}
	var walk func(n *tree.Node, depth int)
	walk = func(n *tree.Node, depth int) {
		if visited[n] {
			return
		}
		visited[n] = true
		folded := m.Folded[n]
		shown := m.visible(n)
		if shown {
			out = append(out, Row{
				Node: n, Depth: depth,
				HasChildren: len(n.Children) > 0,
				Folded:      folded,
			})
			if folded {
				return
			}
		}
		// A hidden node does not hide its children: descend at the same depth
		// so a labelled turn deep in a session still appears, rather than
		// disappearing with its parents.
		next := depth
		if shown {
			next = depth + 1
		}
		for _, c := range n.Children {
			walk(c, next)
		}
	}
	for _, r := range m.Roots {
		walk(r, 0)
	}
	return out
}

func (m *Model) clamp() {
	n := len(m.Rows())
	if n == 0 {
		m.Cursor = 0
		return
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor > n-1 {
		m.Cursor = n - 1
	}
}

func (m *Model) Down() { m.Cursor++; m.clamp() }
func (m *Model) Up()   { m.Cursor--; m.clamp() }

// Selected is the node under the cursor, or nil when the forest is empty.
func (m *Model) Selected() *tree.Node {
	rows := m.Rows()
	if len(rows) == 0 {
		return nil
	}
	m.clamp()
	return rows[m.Cursor].Node
}

// Fold collapses the selected node, or jumps to its parent when it is a leaf.
func (m *Model) Fold() {
	n := m.Selected()
	if n == nil {
		return
	}
	if len(n.Children) > 0 && !m.Folded[n] {
		m.Folded[n] = true
		return
	}
	p := m.parent[n]
	if p == nil {
		return
	}
	for i, r := range m.Rows() {
		if r.Node == p {
			m.Cursor = i
			return
		}
	}
}

// Unfold expands the selected node.
func (m *Model) Unfold() {
	if n := m.Selected(); n != nil {
		delete(m.Folded, n)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -v`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/
git commit -m "$(cat <<'EOF'
feat: TUI view model with folding and navigation

Navigation is pure functions over the model, tested without rendering.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Bubble Tea view, branch action, entrypoint, manifest

**Files:**
- Create: `internal/tui/view.go`, `cmd/herdr-tree/main.go`, `herdr-plugin.toml`, `README.md`
- Modify: `go.mod` (Bubble Tea dependencies)
- Test: `internal/tui/view_test.go`

**Interfaces:**
- Consumes: `tui.Model`, `tui.Row` (Task 12); `adapter.Adapter` (Task 1); `claude.New` (Task 11); `store` (Task 7); `tree.Build` (Task 8); `repo.Root` (Task 1); `herdr.PaneCurrent`, `herdr.OpenTreePane` (Task 10).
- Produces: `tui.Run(a adapter.Adapter, repoRoot string, st *store.Store, sessions []adapter.Session, current string) error`; `tui.renderRow(r Row, selected bool, current string, width int) string`; `tui.confirmText(n *tree.Node, turns, entries int, bytes int64, dstCWD string) string`.

- [ ] **Step 1: Add dependencies**

```bash
cd /home/somliga/projects/herdr-tree
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/lipgloss@latest
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Create `internal/tui/view_test.go`:

```go
package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

func TestRenderRowShowsTitleAndIndent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n2", Title: "i want to discuss the weather"}, SessionID: "82cb69f2-x"}
	got := renderRow(Row{Node: n, Depth: 1}, false, "", 80)
	if !strings.Contains(got, "i want to discuss the weather") {
		t.Fatalf("title missing: %q", got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("depth not indented: %q", got)
	}
}

func TestRenderRowMarksCurrent(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	got := renderRow(Row{Node: n}, false, "sid-a", 80)
	if !strings.Contains(got, "● current") {
		t.Fatalf("current marker missing: %q", got)
	}
}

func TestRenderRowShowsSessionIdOnRoots(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "n1", Title: "x"},
		SessionID: "82cb69f2-e18b-4f86-874a-89e93139324a", IsSessionRoot: true,
	}
	got := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "82cb69f2") {
		t.Fatalf("short session id missing: %q", got)
	}
	if strings.Contains(got, "e18b") {
		t.Fatalf("full uuid should not be shown: %q", got)
	}
}

func TestRenderRowMarksBroken(t *testing.T) {
	n := &tree.Node{SessionID: "sid", IsSessionRoot: true, Broken: true}
	got := renderRow(Row{Node: n}, false, "", 80)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("broken marker missing: %q", got)
	}
}

func TestRenderRowMarksGraft(t *testing.T) {
	n := &tree.Node{
		Node: adapter.Node{ID: "m1", Title: "alt"},
		SessionID: "f2af34a4-x", IsSessionRoot: true, Grafted: true,
	}
	got := renderRow(Row{Node: n, Depth: 2}, false, "", 80)
	if !strings.Contains(got, "↳") {
		t.Fatalf("graft marker missing: %q", got)
	}
}

// fakeAdapter lets the update loop be tested without Herdr or Claude.
type fakeAdapter struct{ resumeErr error }

func (f fakeAdapter) Name() string                                  { return "fake" }
func (f fakeAdapter) Discover(string) ([]adapter.Session, error)    { return nil, nil }
func (f fakeAdapter) Current(adapter.Pane) (string, error)          { return "", nil }
func (f fakeAdapter) Preview(adapter.Session, string) (int, int, int64, error) {
	return 1, 2, 3, nil
}
func (f fakeAdapter) Branch(adapter.Session, string, string) (string, error) { return "new-sid", nil }
func (f fakeAdapter) Resume(string, string) error                            { return f.resumeErr }

func TestFailedResumeKeepsTheOverlayOpen(t *testing.T) {
	// Bubble Tea discards its final frame when leaving the alt screen, so a
	// status set while quitting is never read. A failure must not quit.
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{resumeErr: errors.New("pane split refused")}}

	cmd := resumeCmd(u.a, n)
	msg, ok := cmd().(actionDoneMsg)
	if !ok {
		t.Fatalf("want actionDoneMsg, got %T", cmd())
	}
	if msg.quit {
		t.Fatal("a failed resume must not quit: the message would never be seen")
	}
	if !strings.Contains(msg.status, "pane split refused") {
		t.Fatalf("status does not carry the cause: %q", msg.status)
	}

	after, _ := u.Update(msg)
	got := after.(uiModel)
	if got.quitting {
		t.Fatal("model marked quitting after a failed resume")
	}
	if !strings.Contains(got.View(), "pane split refused") {
		t.Fatalf("the error is not rendered:\n%s", got.View())
	}
}

func TestSuccessfulResumeQuits(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}}

	msg := resumeCmd(u.a, n)().(actionDoneMsg)
	if !msg.quit {
		t.Fatal("a successful resume should close the overlay")
	}
	after, cmd := u.Update(msg)
	if !after.(uiModel).quitting {
		t.Fatal("want quitting set")
	}
	if cmd == nil {
		t.Fatal("want a quit command")
	}
}

func TestKeystrokesAreIgnoredWhileAnActionIsInFlight(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "n1", Title: "x"}, SessionID: "sid-a"}
	u := uiModel{m: New([]*tree.Node{n}), a: fakeAdapter{}, busy: "opening session…"}

	after, _ := u.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if after.(uiModel).confirm != "" {
		t.Fatal("a keystroke started a second action while one was in flight")
	}
	// but ctrl+c must always work
	after2, cmd := u.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !after2.(uiModel).quitting || cmd == nil {
		t.Fatal("ctrl+c must not be swallowed while busy")
	}
}

func TestDstCWDFallsBackWhenTheSessionDirectoryIsGone(t *testing.T) {
	live := t.TempDir()
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	root := t.TempDir()

	u := uiModel{repoRoot: root}

	if got := u.dstCWD(&tree.Node{SessionCWD: live}); got != live {
		t.Fatalf("an existing session directory must be used: got %q want %q", got, live)
	}
	if got := u.dstCWD(&tree.Node{SessionCWD: gone}); got != root {
		t.Fatalf("a removed worktree must fall back to the repo root: got %q want %q", got, root)
	}
	if got := u.dstCWD(&tree.Node{SessionCWD: ""}); got != root {
		t.Fatalf("an empty session cwd must fall back to the repo root: got %q want %q", got, root)
	}
}

func TestConfirmTextNamesWhatIsCarried(t *testing.T) {
	n := &tree.Node{Node: adapter.Node{ID: "u3", Title: "what do you think"}}
	got := confirmText(n, 3, 12, 41984, "/home/somliga/projects/surtr")
	for _, want := range []string{"what do you think", "3 turns", "12 entries", "41 KB", "/home/somliga/projects/surtr"} {
		if !strings.Contains(got, want) {
			t.Fatalf("confirm text missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/tui/ -run 'Render|Confirm' -v`
Expected: FAIL — `undefined: renderRow`.

- [ ] **Step 4: Implement the view**

Create `internal/tui/view.go`:

```go
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// renderRow draws one line. Rendering is deliberately plain text so it can
// be asserted in tests.
func renderRow(r Row, selected bool, currentSession string, width int) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", r.Depth))

	if r.Node.Broken {
		b.WriteString("⚠ ")
	}
	if r.Node.Grafted {
		b.WriteString("↳ ")
	}
	if r.Node.IsSessionRoot {
		b.WriteString(shortID(r.Node.SessionID) + "  ")
	}
	if r.HasChildren && r.Folded {
		b.WriteString("▸ ")
	}

	if r.Node.Label != "" {
		b.WriteString("★ " + r.Node.Label + "  ")
	}
	title := r.Node.Node.Title
	if title == "" && r.Node.Broken {
		title = "transcript unreadable — metadata only"
	}
	b.WriteString(title)

	if r.Node.SessionID != "" && r.Node.SessionID == currentSession {
		b.WriteString("   ● current")
	}
	line := b.String()
	if width > 0 && len([]rune(line)) > width {
		line = string([]rune(line)[:width-1]) + "…"
	}
	return line
}

// confirmText is the branch confirmation, which is where the user is told
// exactly what a graft copies.
func confirmText(n *tree.Node, turns, entries int, size int64, dstCWD string) string {
	return fmt.Sprintf(
		"Branch from:  %q\nCarries:      %d turns · %d entries · %s\nOpens:        split right, unfocused in %s\n\n[enter] branch   [esc] cancel",
		n.Node.Title, turns, entries, humanBytes(size), dstCWD)
}

type uiModel struct {
	m        *Model
	a        adapter.Adapter
	st       *store.Store
	repoRoot string
	current  string
	width    int
	height   int
	confirm  string
	status   string
	busy     string // non-empty while an adapter call is in flight
	quitting bool

	labelling *tree.Node // non-nil while typing a label
	labelText string
}

// actionDoneMsg carries the result of an adapter call back onto the update
// loop. `quit` is set only when the action succeeded — a failure must leave
// the overlay open, because Bubble Tea paints its final frame into the alt
// screen and then discards it on exit, so a message shown while quitting is
// never actually read by anyone.
type actionDoneMsg struct {
	status string
	quit   bool
}

// resumeCmd and branchCmd run OFF the update loop.
//
// herdr's `agent start` waits for the agent to become ready and is bounded at
// 45 seconds. Doing that inside Update freezes every keystroke for the whole
// duration with no feedback and no way to cancel, because Bubble Tea handles
// one message at a time. As a tea.Cmd the work happens on its own goroutine
// and the overlay keeps rendering.
// dstCWD is where a pane for this node should open. Normally the session's
// own directory, so a worktree session reopens in its worktree. But that
// directory can be gone — a removed worktree still shows in the tree by
// design — and opening a pane there fails after the graft has already been
// written. Fall back to the repo root, which exists by construction.
func (u uiModel) dstCWD(n *tree.Node) string {
	if n.SessionCWD != "" {
		if fi, err := os.Stat(n.SessionCWD); err == nil && fi.IsDir() {
			return n.SessionCWD
		}
	}
	return u.repoRoot
}

func resumeCmd(a adapter.Adapter, n *tree.Node, dst string) tea.Cmd {
	return func() tea.Msg {
		if err := a.Resume(n.SessionID, dst); err != nil {
			return actionDoneMsg{status: "could not open session: " + err.Error()}
		}
		return actionDoneMsg{status: "opened " + shortID(n.SessionID), quit: true}
	}
}

func branchCmd(a adapter.Adapter, st *store.Store, n *tree.Node, dst string) tea.Cmd {
	return func() tea.Msg {
		src := adapter.Session{ID: n.SessionID, CWD: n.SessionCWD, Path: n.SessionPath}
		sid, err := a.Branch(src, n.Node.ID, dst)
		if err != nil {
			return actionDoneMsg{status: "branch failed: " + err.Error()}
		}
		// Record the edge before resuming: the transcript now exists, so the
		// branch must survive even if opening it fails.
		st.Add(sid, store.Branch{
			GraftedFrom: store.From{SessionID: n.SessionID, Node: n.Node.ID},
			Title:       n.Node.Title,
			CreatedAt:   time.Now().UTC(),
		})
		if err := st.Save(); err != nil {
			return actionDoneMsg{status: "branched " + shortID(sid) + ", but the tree was not saved: " + err.Error()}
		}
		if err := a.Resume(sid, dst); err != nil {
			return actionDoneMsg{status: "branched " + shortID(sid) + ", but it did not open: " + err.Error()}
		}
		return actionDoneMsg{status: "branched " + shortID(sid), quit: true}
	}
}

func (u uiModel) Init() tea.Cmd { return nil }

func (u uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
	case actionDoneMsg:
		u.busy = ""
		u.status = msg.status
		if msg.quit {
			u.quitting = true
			return u, tea.Quit
		}
		return u, nil
	case tea.KeyMsg:
		if u.busy != "" {
			// An adapter call is in flight. Swallow input rather than queueing
			// a second one, but never trap the user.
			if msg.String() == "ctrl+c" {
				u.quitting = true
				return u, tea.Quit
			}
			return u, nil
		}
		if u.labelling != nil {
			switch msg.Type {
			case tea.KeyEnter:
				n := u.labelling
				u.st.SetLabel(n.SessionID, n.Node.ID, strings.TrimSpace(u.labelText))
				n.Label = strings.TrimSpace(u.labelText)
				if err := u.st.Save(); err != nil {
					u.status = "label not saved: " + err.Error()
				}
				u.labelling, u.labelText = nil, ""
			case tea.KeyEsc:
				u.labelling, u.labelText = nil, ""
			case tea.KeyBackspace:
				if r := []rune(u.labelText); len(r) > 0 {
					u.labelText = string(r[:len(r)-1])
				}
			case tea.KeyRunes, tea.KeySpace:
				u.labelText += msg.String()
			}
			return u, nil
		}
		if u.confirm != "" {
			switch msg.String() {
			case "enter":
				n := u.m.Selected()
				u.confirm = ""
				if n == nil {
					return u, nil
				}
				u.busy = "branching…"
				return u, branchCmd(u.a, u.st, n, u.dstCWD(n))
			case "esc", "q":
				u.confirm = ""
			}
			return u, nil
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			u.quitting = true
			return u, tea.Quit
		case "up", "k":
			u.m.Up()
		case "down", "j":
			u.m.Down()
		case "left", "h":
			u.m.Fold()
		case "right", "l":
			u.m.Unfold()
		case "enter":
			n := u.m.Selected()
			if n == nil {
				return u, nil
			}
			u.busy = "opening session…"
			return u, resumeCmd(u.a, n, u.dstCWD(n))
		case "d":
			u.m.CycleDensity()
		case "L":
			n := u.m.Selected()
			if n == nil || n.Node.ID == "" {
				return u, nil
			}
			u.labelling = n
			u.labelText = n.Label
		case "b":
			if n := u.m.Selected(); n != nil && !n.Broken {
				src := adapter.Session{ID: n.SessionID, CWD: n.SessionCWD, Path: n.SessionPath}
				turns, entries, size, err := u.a.Preview(src, n.Node.ID)
				if err != nil {
					u.status = "cannot branch here: " + err.Error()
				} else {
					u.confirm = confirmText(n, turns, entries, size, u.dstCWD(n))
				}
			}
		}
	}
	return u, nil
}

func (u uiModel) View() string {
	if u.quitting {
		return ""
	}
	if u.labelling != nil {
		return fmt.Sprintf("Label this turn:  %s\n\n  %q\n\n[enter] save   [esc] cancel   (empty clears)\n",
			u.labelText, u.labelling.Node.Title)
	}
	if u.confirm != "" {
		return u.confirm + "\n"
	}
	var b strings.Builder
	rows := u.m.Rows()
	if len(rows) == 0 {
		b.WriteString("No Claude sessions found for this directory.\n")
	}
	for i, r := range rows {
		marker := "  "
		if i == u.m.Cursor {
			marker = "> "
		}
		b.WriteString(marker + renderRow(r, i == u.m.Cursor, u.current, u.width-2) + "\n")
	}
	b.WriteString(fmt.Sprintf("\n↑↓ move  ←→ fold  ⏎ open  b branch  L label  d density:%s  esc close\n", u.m.Density))
	if u.busy != "" {
		b.WriteString(u.busy + "\n")
	}
	if u.status != "" {
		b.WriteString(u.status + "\n")
	}
	return b.String()
}

// Run starts the overlay.
func Run(a adapter.Adapter, repoRoot string, st *store.Store, sessions []adapter.Session, current string) error {
	roots := tree.Build(sessions, st)
	u := uiModel{m: New(roots), a: a, st: st, repoRoot: repoRoot, current: current}
	_, err := tea.NewProgram(u, tea.WithAltScreen()).Run()
	return err
}
```

- [ ] **Step 5: Write the entrypoint**

Create `cmd/herdr-tree/main.go`:

```go
// Command herdr-tree is the plugin binary. "open" is the action Herdr
// invokes from a keybinding; "pane" is the overlay Herdr then opens.
package main

import (
	"fmt"
	"os"

	"herdr-tree/internal/claude"
	"herdr-tree/internal/herdr"
	"herdr-tree/internal/repo"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tui"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "open":
		err = open()
	case "pane":
		err = pane()
	default:
		fmt.Fprintln(os.Stderr, "usage: herdr-tree <open|pane>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdr-tree:", err)
		os.Exit(1)
	}
}

// open asks Herdr to open the overlay in the focused pane's repo.
//
// The adapter check belongs HERE, not in pane(). This runs as a Herdr action
// in the pane the user is looking at, so it can see which agent that pane is
// running. pane() runs inside the overlay Herdr then opens, and an overlay
// pane carries no agent at all — the check there could never fire.
func open() error {
	p, err := herdr.PaneCurrent()
	if err != nil {
		return err
	}
	if p.Agent != "" && p.Agent != "claude" {
		return fmt.Errorf("no adapter for %s", p.Agent)
	}
	root, _, err := repo.Root(p.CWD)
	if err != nil {
		return err
	}
	return herdr.OpenTreePane(root)
}

// pane runs inside the overlay Herdr opened.
func pane() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, isGit, err := repo.Root(cwd)
	if err != nil {
		return err
	}
	a := claude.New()
	sessions, err := a.Discover(root)
	if err != nil {
		return err
	}
	st, err := store.Load(root)
	if err != nil {
		return err
	}

	current := ""
	if p, err := herdr.PaneCurrent(); err == nil {
		if sid, err := a.Current(p); err == nil {
			current = sid
		}
	}
	if !isGit {
		fmt.Fprintf(os.Stderr, "herdr-tree: %s is not a git repository; showing only this directory\n", root)
	}
	return tui.Run(a, root, st, sessions, current)
}
```

- [ ] **Step 6: Write the manifest and README**

Create `herdr-plugin.toml`:

```toml
id = "herdr-tree"
name = "Herdr Agent Tree"
version = "0.1.0"
min_herdr_version = "0.9.0"
description = "Browse this repo's Claude conversation tree and branch from any turn."
platforms = ["linux", "macos"]

[[build]]
command = ["go", "build", "-o", "bin/herdr-tree.exe", "./cmd/herdr-tree"]

[[actions]]
id = "open"
title = "Agent tree: open"
description = "Browse and branch this repository's Claude conversation tree."
contexts = ["pane", "workspace"]
command = ["./bin/herdr-tree.exe", "open"]

[[panes]]
id = "tree"
title = "Agent tree"
placement = "overlay"
command = ["./bin/herdr-tree.exe", "pane"]
```

Create `README.md`:

```markdown
# herdr-tree

A Herdr plugin that draws a repo-wide tree of Claude Code conversation turns
and starts a new Claude session continuing from any turn in it.

## Install

```bash
herdr plugin link /home/somliga/projects/herdr-tree
```

Then bind a key in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+t"
type = "plugin_action"
command = "herdr-tree.open"
```

## Keys

| Key | Action |
|-----|--------|
| ↑ ↓ | Move |
| ← → | Fold / unfold |
| ⏎ | Open the selected session in a new pane |
| b | Branch from the selected turn |
| esc | Close |

## How branching works

Claude Code has no supported way to resume at a specific message, so
branching writes a new transcript containing only the ancestor chain of the
chosen turn, then resumes it. See
`docs/superpowers/specs/2026-09-21-herdr-tree-design.md`.

The transcript format is undocumented and verified against Claude Code
2.1.278. The plugin refuses to branch rather than guess when it sees a
format it has not been validated against.
```

- [ ] **Step 7: Run everything**

Run: `go build ./... && go vet ./... && go test ./... -v`
Expected: build clean, vet clean, all tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/ cmd/ herdr-plugin.toml README.md go.mod go.sum
git commit -m "$(cat <<'EOF'
feat: Bubble Tea view, branch action, plugin manifest

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: End-to-end verification against real Claude Code

**Files:**
- Create: `scripts/verify-graft.sh`

**Interfaces:**
- Consumes: the built binary and `internal/claude` behavior from Tasks 5-6.
- Produces: nothing importable. This is the manual check that catches Claude Code changing its format.

This is the only test that exercises the real coupling. It costs a few cents per run and must not go in CI.

- [ ] **Step 1: Write the script**

Create `scripts/verify-graft.sh`:

```bash
#!/usr/bin/env bash
# Manual end-to-end check: does Claude Code still resume a transcript we
# wrote? Costs a small Haiku call. Not for CI.
#
# Usage: scripts/verify-graft.sh <source-session.jsonl> <node-uuid> <kept-topic> <pruned-topic>
set -euo pipefail

src=${1:?source transcript}
node=${2:?node uuid to graft at}
kept=${3:?a word that should survive}
pruned=${4:?a word that should NOT survive}

work=$(mktemp -d)
graft=""
# Clean up BOTH the temp cwd and the session this writes into Claude Code's
# own store. Without the second part every run leaves a directory behind in
# ~/.claude/projects and clutters the /resume picker.
#
# rmdir, never rm -rf: it refuses on a non-empty directory, so a computed path
# can never delete something unexpected. That conservatism earned its keep —
# the first version of this cleanup failed precisely because Claude Code had
# created a memory/ subdirectory, and rmdir surfaced that instead of quietly
# deleting it.
cleanup() {
  rm -rf "$work"
  if [ -n "$graft" ]; then
    dir=$(dirname "$graft")
    rm -f "$graft"
    # Claude Code also creates an empty memory/ subdirectory per project.
    rmdir "$dir/memory" 2>/dev/null || true
    rmdir "$dir" 2>/dev/null || true
  fi
}
trap cleanup EXIT
mkdir -p "$work/cwd"

# graftcheck prints the session id on the first line and the file it wrote on
# the second, so the cleanup above knows what to remove.
out=$(go run ./cmd/graftcheck "$src" "$node" "$work/cwd")
sid=$(printf '%s\n' "$out" | sed -n 1p)
graft=$(printf '%s\n' "$out" | sed -n 2p)
echo "grafted session: $sid"

reply=$(cd "$work/cwd" && claude -p --model claude-haiku-4-5-20251001 \
  --resume "$sid" \
  "List every topic we have discussed so far as a short bullet list. Do not use any tools." \
  < /dev/null)

echo "--- reply ---"
echo "$reply"
echo "-------------"

fail=0
grep -qi -- "$kept"   <<<"$reply" || { echo "FAIL: kept topic '$kept' missing"; fail=1; }
grep -qi -- "$pruned" <<<"$reply" && { echo "FAIL: pruned topic '$pruned' leaked"; fail=1; }
[ "$fail" = 0 ] && echo "PASS: graft truncates history correctly"
exit "$fail"
```

```bash
chmod +x scripts/verify-graft.sh
```

- [ ] **Step 2: Write the helper command the script calls**

Create `cmd/graftcheck/main.go`:

```go
// Command graftcheck grafts a transcript and prints the new session id.
// It exists for scripts/verify-graft.sh and is not part of the plugin.
package main

import (
	"fmt"
	"os"

	"herdr-tree/internal/claude"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: graftcheck <src.jsonl> <node-uuid> <dst-cwd>")
		os.Exit(2)
	}
	sid, path, err := claude.Graft(os.Args[1], os.Args[2], os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "graft:", err)
		os.Exit(1)
	}
	// Line 1 is the session id, line 2 is the file written. The script reads
	// both so it can clean up the session afterwards.
	fmt.Println(sid)
	fmt.Println(path)
}
```

- [ ] **Step 3: Run it against a real session**

Pick any multi-turn session of your own, find a turn uuid roughly in the middle, and choose a word from an early turn and a word from a later one.

```bash
go build ./...
ls ~/.claude/projects/*/          # pick a transcript
scripts/verify-graft.sh <path.jsonl> <node-uuid> <early-word> <late-word>
```

Expected: `PASS: graft truncates history correctly`.

If it fails with the pruned topic leaking, `Select` is keeping too much. If the kept topic is missing, it is keeping too little. Either way the golden test in Task 5 needs a case added before changing the implementation.

- [ ] **Step 4: Commit**

```bash
git add scripts/ cmd/graftcheck/
git commit -m "$(cat <<'EOF'
test: manual end-to-end graft verification

The only check that catches Claude Code changing its transcript format.
Costs a real API call; deliberately not in CI.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: Install and use it

**Files:** none.

- [ ] **Step 1: Link the plugin**

```bash
herdr plugin link /home/somliga/projects/herdr-tree
herdr plugin list --plugin herdr-tree --json
```

Expected: the plugin is listed and enabled, and `bin/herdr-tree.exe` exists (Herdr runs the build step on link).

- [ ] **Step 2: Bind the key**

Add to `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+t"
type = "plugin_action"
command = "herdr-tree.open"
```

```bash
herdr server reload-config
```

- [ ] **Step 3: Open it**

Press `Ctrl+B` then `t` in a pane inside a repo that has Claude sessions.

Expected: the overlay lists that repo's sessions as turn chains, with the
current session marked `● current`.

- [ ] **Step 4: Branch once, deliberately**

Select an earlier turn, press `b`, read the confirmation, press enter.

Expected: a new pane opens to the right with Claude resumed at that point,
the original session is unchanged, and pressing `Ctrl+B t` again shows the
new branch nested under the turn you chose.

- [ ] **Step 5: Confirm the source was not modified**

```bash
ls -la ~/.claude/projects/*/          # the new session is 0600
```

Expected: the grafted file is mode `-rw-------`, and the source transcript's
size and mtime are unchanged.

---

## Notes for the implementer

**The riskiest code is Task 5.** Everything else is plumbing. If `Select`
keeps the wrong set, the user gets a session that looks fine and contains
the wrong conversation. Add a golden case before touching it.

**Do not "fix" the synthetic-turn asymmetry.** `Turns` filters
`"Continue from where you left off."` because the user did not type it;
`Select` keeps it because it is real conversation content. That difference
is intentional.

**Do not delete session files.** Not orphans from a failed branch, not
one-turn stubs, not sessions whose transcript will not parse. They are the
user's data and a resumable session is worth more than a tidy directory.

**Store nothing that can be derived.** The store holds graft edges and
nothing else. If you find yourself caching titles or turn counts into
tree.json, the tree has started lying about transcripts it no longer reads.
