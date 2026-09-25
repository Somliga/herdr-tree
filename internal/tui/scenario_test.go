package tui

// This file imports internal/claude. The "internal/tui must not import
// internal/claude" rule protects the production binary's layering; a
// test-only import creates no cycle, and it is the only way to drive the
// real overlay against the real adapter (controller ruling, Task 16).
//
// Every scenario runs on real files: transcripts in a temp CLAUDE_PROJECTS_DIR
// whose cwd is a temp git repo, a real store under a temp
// HERDR_PLUGIN_CONFIG_DIR, the real claude adapter (Discover, Widen, Preview,
// Splice, Graft, Summarise), tree.Build and this package's Update. Only three
// things are stood in for: `claude` is a stub on PATH that prints a fixed
// summary (so the real Summarise path runs and spends nothing), `herdr` is a
// stub on PATH that fails (so nothing can reach a live Herdr), and Resume,
// which would ask herdr for a pane, is recorded instead.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/claude"
	"herdr-tree/internal/store"
	"herdr-tree/internal/tree"
)

// world is one scenario's filesystem, adapter and panes.
type world struct {
	t     *testing.T
	repo  string // the sessions' cwd, a git repo
	proj  string // CLAUDE_PROJECTS_DIR
	stubs string // where the stub claude and herdr live
	a     *recAdapter
	h     *paneLog
	clock time.Time
}

// recAdapter is the real claude adapter with Resume recorded, since Resume
// is the one method that goes to herdr.
type recAdapter struct {
	adapter.Adapter
	h *paneLog
}

func (r *recAdapter) Resume(sid, _ string, focus bool) error {
	r.h.calls = append(r.h.calls, fmt.Sprintf("resume %s focus=%v", sid, focus))
	return nil
}

// paneLog is herdr as the injected live/closePane funcs see it: which pane
// holds each session and its agent_status. Every call is logged in order,
// Resume's too, so a handover's step order can be asserted.
type paneLog struct {
	panes map[string][2]string // session id -> {pane, status}
	calls []string
}

func (p *paneLog) live(sid string) (string, string, error) {
	p.calls = append(p.calls, "live "+sid)
	v := p.panes[sid]
	return v[0], v[1], nil
}

func (p *paneLog) close(pane string) error {
	p.calls = append(p.calls, "close "+pane)
	return nil
}

func newWorld(t *testing.T) *world {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	w := &world{t: t, repo: repo, proj: t.TempDir(), stubs: t.TempDir(),
		h: &paneLog{panes: map[string][2]string{}}, clock: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	w.a = &recAdapter{Adapter: claude.New(), h: w.h}
	t.Setenv("CLAUDE_PROJECTS_DIR", w.proj)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	// The summary is fixed and says nothing about the prompt, so a status
	// line carrying it would be caught by the scenarios' text assertions.
	stub := map[string]string{
		"claude": "#!/bin/sh\necho call >> " + filepath.Join(w.stubs, "claude-calls") + "\nprintf 'state: the fixed summary\\nnext: carry on\\n'\n",
		"herdr":  "#!/bin/sh\necho \"stub herdr refused: $*\" >&2\nexit 1\n",
	}
	for name, body := range stub {
		if err := os.WriteFile(filepath.Join(w.stubs, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", w.stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	return w
}

// summaries is how many times the stub claude was called.
func (w *world) summaries() int {
	b, _ := os.ReadFile(filepath.Join(w.stubs, "claude-calls"))
	return strings.Count(string(b), "call")
}

func (w *world) tick() string {
	w.clock = w.clock.Add(time.Second)
	return w.clock.Format(time.RFC3339)
}

// turnLines is one turn as Claude Code writes it: the prompt, a Bash call,
// its result, and the reply. uuids are <id>-p, -c, -x and -r, so a test can
// name any row; parent is the entry the prompt follows ("" for a root).
func (w *world) turnLines(sid, id, parent string) []map[string]any {
	base := func(typ, uuid, parent string) map[string]any {
		m := map[string]any{"type": typ, "uuid": uuid, "sessionId": sid, "cwd": w.repo,
			"version": "2.1.278", "timestamp": w.tick(), "isSidechain": false, "userType": "external"}
		if parent == "" {
			m["parentUuid"] = nil
		} else {
			m["parentUuid"] = parent
		}
		return m
	}
	p := base("user", id+"-p", parent)
	p["origin"] = map[string]any{"kind": "human"}
	p["message"] = map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "prompt " + id}}}
	c := base("assistant", id+"-c", id+"-p")
	c["requestId"] = "req-" + id + "-1"
	c["message"] = map[string]any{"role": "assistant", "content": []any{map[string]any{
		"type": "tool_use", "id": "toolu_" + id, "name": "Bash", "input": map[string]any{"command": "echo " + id}}}}
	x := base("user", id+"-x", id+"-c")
	x["toolUseResult"] = map[string]any{"stdout": id}
	x["message"] = map[string]any{"role": "user", "content": []any{map[string]any{
		"type": "tool_result", "tool_use_id": "toolu_" + id, "content": id}}}
	r := base("assistant", id+"-r", id+"-x")
	r["requestId"] = "req-" + id + "-2"
	r["message"] = map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "reply " + id}}}
	return []map[string]any{p, c, x, r}
}

func (w *world) appendLines(path string, lines []map[string]any) {
	w.t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			w.t.Fatal(err)
		}
		f.Write(append(b, '\n'))
	}
}

// trunk writes a new session sid with a preamble and one turn per id.
func (w *world) trunk(sid string, ids ...string) {
	w.t.Helper()
	dir := filepath.Join(w.proj, claude.SlugFor(w.repo))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		w.t.Fatal(err)
	}
	pre := map[string]any{"type": "attachment", "uuid": sid[:4] + "-pre", "parentUuid": nil, "sessionId": sid,
		"cwd": w.repo, "version": "2.1.278", "timestamp": w.tick(), "attachment": map[string]any{"kind": "reminder"}}
	w.appendLines(filepath.Join(dir, sid+".jsonl"), []map[string]any{pre})
	w.typeInto(sid, ids...)
}

// typeInto appends turns to sid's transcript, after its tip, as Claude Code
// does when the user types into it.
func (w *world) typeInto(sid string, ids ...string) {
	w.t.Helper()
	path := w.path(sid)
	es, _, err := claude.ParseFile(path)
	if err != nil {
		w.t.Fatal(err)
	}
	tip := ""
	for _, e := range es {
		if u := e.UUID(); u != "" && (e.Type() == "user" || e.Type() == "assistant" || e.Type() == "attachment") {
			tip = u
		}
	}
	for _, id := range ids {
		w.appendLines(path, w.turnLines(sid, id, tip))
		tip = id + "-r"
	}
}

func (w *world) path(sid string) string {
	w.t.Helper()
	m, _ := filepath.Glob(filepath.Join(w.proj, "*", sid+".jsonl"))
	if len(m) != 1 {
		w.t.Fatalf("transcript of %s: %v", sid, m)
	}
	return m[0]
}

// open is Run without the terminal: Discover, Load, Build, scoped to current.
func (w *world) open(current string) uiModel {
	w.t.Helper()
	sessions, err := w.a.Discover(w.repo)
	if err != nil {
		w.t.Fatal(err)
	}
	st, err := store.Load(w.repo)
	if err != nil {
		w.t.Fatal(err)
	}
	roots := tree.Build(sessions, st)
	u := uiModel{m: New(roots), a: w.a, st: st, repoRoot: w.repo, current: current, roots: roots,
		live: w.h.live, closePane: w.h.close}
	u.rebuild()
	return u
}

// drive feeds msgs to Update and runs every command it returns on the spot,
// feeding its message back in, until the loop settles or quits.
func drive(t *testing.T, u uiModel, msgs ...tea.Msg) uiModel {
	t.Helper()
	for _, m := range msgs {
		next, cmd := u.Update(m)
		u = next.(uiModel)
		for cmd != nil {
			out := cmd()
			if _, ok := out.(tea.QuitMsg); ok {
				break
			}
			next, cmd = u.Update(out)
			u = next.(uiModel)
		}
		if strings.Contains(u.status, "fixed summary") {
			t.Errorf("a status line carries the summary: %q", u.status)
		}
	}
	return u
}

func typed(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, key(r))
	}
	return out
}

// unfold opens every section, as → on each would.
func unfold(u uiModel) { u.m.Folded = map[*tree.Node]bool{} }

// rowOf is the index of sid's row for entry id, -1 if it has none.
func rowOf(u uiModel, sid, id string) int {
	unfold(u)
	for i, r := range u.m.Rows() {
		if r.Node.SessionID == sid && r.Node.Node.ID == id {
			return i
		}
	}
	return -1
}

func cursorTo(t *testing.T, u uiModel, sid, id string) uiModel {
	t.Helper()
	i := rowOf(u, sid, id)
	if i < 0 {
		t.Fatalf("no row %s of %s on screen:\n%s", id, shortID(sid), strings.Join(screen(u), "\n"))
	}
	u.m.Cursor = i
	return u
}

// screen is every row as the user sees it, unfolded, with its cut marker.
func screen(u uiModel) []string {
	unfold(u)
	var out []string
	for _, r := range u.m.Rows() {
		line, _ := renderRow(r, false, u.current, 0)
		out = append(out, line+cutNote(r.Node))
	}
	return out
}

// allOf is u with every session in view.
func allOf(u uiModel) uiModel {
	u.scopeAll = true
	u.rebuild()
	return u
}

// branch is ⏎ then confirm on sid's entry id, in an overlay opened for
// current. It returns the new session, which the overlay opened (unfocused).
func (w *world) branch(current, sid, id string) string {
	w.t.Helper()
	u := cursorTo(w.t, allOf(w.open(current)), sid, id)
	u = drive(w.t, u, enter)
	if !strings.Contains(u.confirm, "Continue from") {
		w.t.Fatalf("no branch confirmation: %q / %q", u.confirm, u.status)
	}
	before := len(w.h.calls)
	u = drive(w.t, u, enter)
	if !u.quitting || len(w.h.calls) != before+1 || !strings.HasSuffix(w.h.calls[before], "focus=false") {
		w.t.Fatalf("branch did not open unfocused and quit: %q %v", u.status, w.h.calls[before:])
	}
	return strings.Fields(w.h.calls[before])[1]
}

// selectRange fixes a range from sid's entry from to its entry to and
// chooses option opt of the range menu (0 squash, 1 squash into…, 2 drop).
func selectRange(t *testing.T, u uiModel, sid, from, to string, opt int) uiModel {
	t.Helper()
	u = cursorTo(t, u, sid, to)
	u = drive(t, u, key('s'))
	u = cursorTo(t, u, sid, from)
	u = drive(t, u, enter)
	if u.menu != "range" {
		t.Fatalf("no range menu: %q", u.status)
	}
	for i := 0; i < opt; i++ {
		u = drive(t, u, down)
	}
	return drive(t, u, enter)
}

// replacement is the session that now stands in sid's place.
func (w *world) replacement(sid string) string {
	w.t.Helper()
	st, err := store.Load(w.repo)
	if err != nil {
		w.t.Fatal(err)
	}
	r := st.Resolve(sid)
	if r == sid {
		w.t.Fatalf("%s was not replaced", shortID(sid))
	}
	return r
}

// checkNoCopies fails if any entry renders twice: a branch's copies of the
// line it left share that line's uuids, so a repeated id is a repeated turn.
func checkNoCopies(t *testing.T, u uiModel) {
	t.Helper()
	unfold(u)
	seen := map[string]string{}
	for _, r := range u.m.Rows() {
		id := r.Node.Node.ID
		if id == "" {
			continue
		}
		if s, dup := seen[id]; dup {
			t.Errorf("entry %s renders in %s and again in %s:\n%s", id, shortID(s), shortID(r.Node.SessionID), strings.Join(screen(u), "\n"))
			return
		}
		seen[id] = r.Node.SessionID
	}
}

// checkHangsUnder asserts child's first row is its first own entry, marked
// ↳ <child>, drawn right after parent's entry at and indented under it — a
// branch is always indented under the turn it left (§5.3d), on the trunk or
// not.
func checkHangsUnder(t *testing.T, u uiModel, child, first, parent, at string) {
	t.Helper()
	unfold(u)
	rows := u.m.Rows()
	p, c := rowOf(u, parent, at), -1
	for i, r := range rows {
		if r.Node.SessionID == child {
			c = i
			break
		}
	}
	if p < 0 || c < 0 {
		t.Fatalf("%s (row %d) or %s's %s (row %d) not on screen:\n%s", shortID(child), c, shortID(parent), at, p, strings.Join(screen(u), "\n"))
	}
	text := screen(u)[c]
	if rows[c].Node.Node.ID != first || !strings.Contains(text, "↳ "+shortID(child)) {
		t.Errorf("%s starts at %q, want %s marked ↳:\n%s", shortID(child), text, first, strings.Join(screen(u), "\n"))
	}
	// A graft is lifted to render as if it hung directly off its turn's own
	// HEAD (§5.3d, and orderedChildren's bodyGrafts lift): body rows first
	// (in Build's own order — the entry named "at" plus any added since,
	// such as a later squash's seed), then every graft, so the child's row
	// comes right after the LAST body row of that turn, not necessarily
	// right after "at" itself, and exactly one level under the head.
	head := p
	for head > 0 && !rows[head].Node.IsHead {
		head--
	}
	after := head + 1
	for after < len(rows) && rows[after].Node.SessionID == rows[head].Node.SessionID && !rows[after].Node.IsHead {
		after++
	}
	if c != after || rows[c].Depth != rows[head].Depth+1 {
		t.Errorf("%s at row %d depth %d, want right after %s's turn's body (row %d, head %s at row %d depth %d), one level under it:\n%s",
			shortID(child), c, rows[c].Depth, shortID(parent), after, rows[head].Node.Node.ID, head, rows[head].Depth, strings.Join(screen(u), "\n"))
	}
}

// checkVisibleFolded asserts sid has at least one row in the model's CURRENT
// fold state — deliberately never calling unfold first. checkHangsUnder,
// rowOf and checkLines all unfold before looking, so none of them can catch
// a branch that a folded head is hiding (the common case since Task 20's
// whole-turn rule almost always grafts on a body row, not the head).
func checkVisibleFolded(t *testing.T, u uiModel, sid string) {
	t.Helper()
	for _, r := range u.m.Rows() {
		if r.Node.SessionID == sid {
			return
		}
	}
	t.Errorf("%s has no visible row in the default fold state:\n%s", shortID(sid), strings.Join(screen(u), "\n"))
}

// checkLines asserts exactly the named sessions have rows.
func checkLines(t *testing.T, u uiModel, want ...string) {
	t.Helper()
	unfold(u)
	got := map[string]bool{}
	for _, r := range u.m.Rows() {
		got[r.Node.SessionID] = true
	}
	wantSet := map[string]bool{}
	for _, s := range want {
		wantSet[s] = true
		if !got[s] {
			t.Errorf("%s is not on screen", shortID(s))
		}
	}
	for s := range got {
		if !wantSet[s] {
			t.Errorf("%s is on screen and should not be:\n%s", shortID(s), strings.Join(screen(u), "\n"))
		}
	}
}

func checkScopes(t *testing.T, u uiModel, sids ...string) {
	t.Helper()
	for _, s := range sids {
		if ScopeTo(u.roots, s) == nil {
			t.Errorf("ScopeTo(%s) finds nothing", shortID(s))
		}
	}
}

func rowText(u uiModel, sid, id string) string {
	if i := rowOf(u, sid, id); i >= 0 {
		return screen(u)[i]
	}
	return ""
}

const (
	sidT = "a1a1a1a1-0000-4000-8000-000000000001"
	sidU = "b2b2b2b2-0000-4000-8000-000000000002"
)

// branchesOfBranches builds scenario A's forest: T (t1..t5), B off T's turn
// 2 with b1..b3 typed into it, C off B's own turn b1 with c1.
func branchesOfBranches(w *world) (b, c string) {
	w.trunk(sidT, "t1", "t2", "t3", "t4", "t5")
	b = w.branch(sidT, sidT, "t2-r")
	w.typeInto(b, "b1", "b2", "b3")
	c = w.branch(b, b, "b1-r")
	w.typeInto(c, "c1")
	return b, c
}

// A. A branch of a branch renders each line from where it diverges.
func TestScenarioBranchOfABranch(t *testing.T) {
	w := newWorld(t)
	b, c := branchesOfBranches(w)

	for _, current := range []string{sidT, b, c} {
		u := allOf(w.open(current))
		checkVisibleFolded(t, u, b) // before checkLines/checkHangsUnder unfold everything
		checkVisibleFolded(t, u, c)
		checkLines(t, u, sidT, b, c)
		checkHangsUnder(t, u, b, "b1-p", sidT, "t2-r")
		checkHangsUnder(t, u, c, "c1-p", b, "b1-r")
		checkNoCopies(t, u)
		checkScopes(t, u, sidT, b, c)
	}
	// Scoped to T, the whole family is T's tree.
	u := w.open(sidT)
	checkLines(t, u, sidT, b, c)
}

// A2. ⏎ on a folded head row grafts after the WHOLE turn it belongs to
// (§2.5b), not at the prompt itself: branching on t2's PROMPT row must land
// exactly where branching on its REPLY row would — right under t2-r, with
// t2-r itself not duplicated into the branch as a visible copy.
func TestScenarioBranchStartsAfterTheWholeTurn(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "t1", "t2", "t3")

	b := w.branch(sidT, sidT, "t2-p") // the folded head row, not its reply

	es, _, err := claude.ParseFile(w.path(b))
	if err != nil {
		t.Fatal(err)
	}
	last := ""
	for _, e := range es {
		if u := e.UUID(); u != "" {
			last = u
		}
	}
	if last != "t2-r" {
		t.Fatalf("branch file's last copied entry is %q, want t2-r (t2's whole turn, not just its prompt)", last)
	}

	w.typeInto(b, "b1")
	u := allOf(w.open(b))
	checkLines(t, u, sidT, b)
	checkHangsUnder(t, u, b, "b1-p", sidT, "t2-r")
	checkNoCopies(t, u)
}

// A3. §5.3d: the branch off BULLDOG (t2) is always indented under it, bar
// and all, and TRIPPLEDIP (t3) — the trunk's own tail once the user is on
// the branch — stays at the root's depth with no bar. Deliberately NOT
// unfolded: BULLDOG's whole-turn graft (Task 20, §2.5b) lands on its REPLY,
// a body row, and New() folds BULLDOG-p by default. A graft attached under a
// folded head's body must still render — Rows()'s fold only hides a folded
// head's own SAME-SESSION body rows, never a graft, wherever in that turn it
// physically attached — so this exercises the default (folded) state on
// purpose, not through checkHangsUnder/rowOf/checkLines, which all unfold
// first and would never catch this.
func TestScenarioBranchAtBulldogIndentsAndTheTailDoesNot(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "APPLE", "BULLDOG", "TRIPPLEDIP")

	b := w.branch(sidT, sidT, "BULLDOG-p")
	w.typeInto(b, "BRANCH1")

	u := allOf(w.open(b))
	rows := u.m.Rows() // no unfold: the default (folded) state

	find := func(sid, id string) (Row, bool) {
		for _, r := range rows {
			if r.Node.SessionID == sid && r.Node.Node.ID == id {
				return r, true
			}
		}
		return Row{}, false
	}
	bulldogRow, ok := find(sidT, "BULLDOG-p")
	if !ok {
		t.Fatalf("BULLDOG-p not on screen folded:\n%s", strings.Join(screen(u), "\n"))
	}
	branchRow, ok := find(b, "BRANCH1-p")
	if !ok {
		t.Fatalf("the branch is invisible while BULLDOG is folded (its graft point, BULLDOG's reply, is a body row):\n%s", strings.Join(screen(u), "\n"))
	}
	tailRow, ok := find(sidT, "TRIPPLEDIP-p")
	if !ok {
		t.Fatalf("TRIPPLEDIP-p not on screen folded:\n%s", strings.Join(screen(u), "\n"))
	}

	if branchRow.Depth != bulldogRow.Depth+1 || !branchRow.OnTrunk {
		t.Fatalf("the branch at BULLDOG must be indented exactly one level under it with the bar: %+v vs BULLDOG's %+v", branchRow, bulldogRow)
	}
	if tailRow.Depth != bulldogRow.Depth || tailRow.OnTrunk {
		t.Fatalf("TRIPPLEDIP is the abandoned tail: root depth, no bar: %+v", tailRow)
	}
}

// A4. Round-2 review finding: a lifted graft's m.parent still names the
// body row it physically attached to (BULLDOG's reply), not the head it now
// renders under (BULLDOG-p) — orderedChildren lifts it for RENDERING, but
// Build's own Children graph, which m.parent walks, is unchanged. Fold() on
// the branch's own first row must still land on BULLDOG-p, folded or not.
//
// No typeInto after w.branch: nothing new was written into the branch, so
// its own kept row (attachPoint's "nothing new" fallback) is the copy of
// BULLDOG's reply itself — a LEAF, so Fold() always jumps rather than
// folding it first, in every fold state.
func TestFoldOnABranchLandsOnItsHead(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "APPLE", "BULLDOG", "TRIPPLEDIP")
	b := w.branch(sidT, sidT, "BULLDOG-p")

	check := func(t *testing.T, u uiModel) {
		t.Helper()
		i := -1
		for j, r := range u.m.Rows() {
			if r.Node.SessionID == b {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("the branch's own row is not on screen:\n%s", strings.Join(screen(u), "\n"))
		}
		u.m.Cursor = i
		u.m.Fold()
		cur := u.m.Rows()[u.m.Cursor]
		if cur.Node.SessionID != sidT || cur.Node.Node.ID != "BULLDOG-p" {
			t.Fatalf("Fold() on the branch landed on %s %q, want BULLDOG-p:\n%s",
				shortID(cur.Node.SessionID), cur.Node.Node.ID, strings.Join(screen(u), "\n"))
		}
	}

	t.Run("folded", func(t *testing.T) {
		check(t, allOf(w.open(b))) // the default state
	})
	t.Run("unfolded", func(t *testing.T) {
		u := allOf(w.open(b))
		unfold(u)
		check(t, u)
	})
}

// A branch of a branch of a branch with nothing new of its own: C's one
// kept row (its copy of b1-r) sits under C's Superseded copy of b1-p, and D
// hangs on that kept row. The UI never offers to branch from a line's tip, so
// D's graft is written by hand, as a hand-edited store could. D renders
// beside that kept row's section head, C's Superseded b1-p, which has no row;
// stepping up must go past it to the head row it sits under, B's b1-p.
func TestFoldOnABranchOfABranchLandsOnARowYouCanSee(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "APPLE", "BULLDOG", "TRIPPLEDIP")
	b := w.branch(sidT, sidT, "BULLDOG-p")
	w.typeInto(b, "b1", "b2")
	c := w.branch(b, b, "b1-p")
	d := "d4d4d4d4-0000-4000-8000-000000000004"
	body, err := os.ReadFile(w.path(c))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(w.path(c)), d+".jsonl"),
		[]byte(strings.ReplaceAll(string(body), c, d)), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(w.repo)
	if err != nil {
		t.Fatal(err)
	}
	st.Add(d, store.Branch{GraftedFrom: store.From{SessionID: c, Node: "b1-r"}})
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, u uiModel) {
		t.Helper()
		i := -1
		for j, r := range u.m.Rows() {
			if r.Node.SessionID == d {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("D's own row is not on screen:\n%s", strings.Join(screen(u), "\n"))
		}
		u.m.Cursor = i
		u.m.Fold()
		cur := u.m.Rows()[u.m.Cursor]
		if cur.Node.SessionID != b || cur.Node.Node.ID != "b1-p" {
			t.Fatalf("Fold() on D landed on %s %q, want B's b1-p:\n%s",
				shortID(cur.Node.SessionID), cur.Node.Node.ID, strings.Join(screen(u), "\n"))
		}
	}

	t.Run("folded", func(t *testing.T) {
		check(t, allOf(w.open(d)))
	})
	t.Run("unfolded", func(t *testing.T) {
		u := allOf(w.open(d))
		unfold(u)
		check(t, u)
	})
}

// B. squash into… moves a branch's turns into the trunk, then the trunk's
// into a branch of that branch.
func TestScenarioSquashIntoAcrossBranches(t *testing.T) {
	w := newWorld(t)
	b, c := branchesOfBranches(w)
	// A fresh, never-unfolded uiModel, before selectRange's cursorTo unfolds
	// the one this test drives.
	checkVisibleFolded(t, allOf(w.open(sidT)), b)
	checkVisibleFolded(t, allOf(w.open(sidT)), c)

	// B's own b2..b3 → squash into… → T's last turn → merge here → confirm.
	u := selectRange(t, allOf(w.open(b)), b, "b2-p", "b3-r", 1)
	if u.folding == nil {
		t.Fatalf("not in target mode: %q", u.status)
	}
	u = drive(t, cursorTo(t, u, sidT, "t5-r"), enter)
	if u.menu != "place" {
		t.Fatalf("no place menu: %q", u.status)
	}
	u = drive(t, u, enter) // merge here
	if !strings.Contains(u.confirm, "merged into "+shortID(sidT)) || !strings.Contains(u.confirm, "dropped from "+shortID(b)) {
		t.Fatalf("confirmation:\n%s", u.confirm)
	}
	u = drive(t, u, enter)
	if w.summaries() != 1 {
		t.Fatalf("%d summary calls, want 1; status %q", w.summaries(), u.status)
	}
	t1, b1 := w.replacement(sidT), w.replacement(b)
	if !strings.HasPrefix(u.status, "squashed into "+shortID(sidT)+", dropped 2 turns from "+shortID(b)) {
		t.Fatalf("status %q", u.status)
	}

	u = allOf(u)
	checkLines(t, u, t1, b1, c)
	var merged string
	for _, l := range screen(u) {
		if strings.Contains(l, "⤶ merged from "+shortID(b)) {
			merged = l
		}
	}
	if merged == "" {
		t.Errorf("T's replacement has no ⤶ merged-from row:\n%s", strings.Join(screen(u), "\n"))
	}
	if got := rowText(u, b1, "b1-r"); !strings.Contains(got, "✂ 2 turns dropped") {
		t.Errorf("B's replacement's last row %q, want the ✂ marker", got)
	}
	checkHangsUnder(t, u, b1, "b1-p", t1, "t2-r")
	checkHangsUnder(t, u, c, "c1-p", b1, "b1-r")
	checkNoCopies(t, u)
	checkScopes(t, u, t1, b1, c)
	for _, id := range []string{"b2-p", "b3-p"} {
		if rowOf(u, b1, id) >= 0 {
			t.Errorf("%s still in B's replacement", id)
		}
	}

	// Now T's t3..t4 → squash into… → C's tip → branch here → confirm.
	u = selectRange(t, u, t1, "t3-p", "t4-r", 1)
	u = drive(t, cursorTo(t, u, c, "c1-r"), enter)
	if u.menu != "place" {
		t.Fatalf("no place menu: %q", u.status)
	}
	u = drive(t, u, down, enter) // branch here
	if !strings.Contains(u.confirm, "branches at "+shortID(c)) {
		t.Fatalf("confirmation:\n%s", u.confirm)
	}
	u = drive(t, u, enter)
	t2 := w.replacement(t1)
	if !strings.HasPrefix(u.status, "squashed into "+shortID(c)+", dropped 2 turns from "+shortID(t1)) {
		t.Fatalf("status %q", u.status)
	}
	st, _ := store.Load(w.repo)
	var d string
	for id, br := range st.Branches {
		if br.GraftedFrom.SessionID == c {
			d = id
		}
	}
	if d == "" {
		t.Fatal("no branch recorded off C")
	}

	u = allOf(u)
	checkLines(t, u, t2, b1, c, d)
	if got := rowText(u, t2, "t5-p"); !strings.Contains(got, "✂ 2 turns dropped before this") {
		t.Errorf("T's row after the drop %q, want the ✂ marker", got)
	}
	checkHangsUnder(t, u, b1, "b1-p", t2, "t2-r")
	checkHangsUnder(t, u, c, "c1-p", b1, "b1-r")
	rows := u.m.Rows()
	for i, r := range rows {
		if r.Node.SessionID == d {
			if r.Node.Node.Kind != adapter.KindSummaryImport || !strings.Contains(screen(u)[i], "↳ "+shortID(d)) {
				t.Errorf("the new branch starts at %q, want its ⤶ merged-from seed marked ↳", screen(u)[i])
			}
			if p := rowOf(u, c, "c1-r"); i != p+1 {
				t.Errorf("the new branch is at row %d, want right under C's c1-r (row %d)", i, p)
			}
			break
		}
	}
	checkNoCopies(t, u)
	checkScopes(t, u, t2, b1, c, d)
}

// C. Three replacements of one line in place, with branches off turns 2
// and 4.
func TestScenarioAChainOfReplacements(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "t1", "t2", "t3", "t4", "t5")
	b := w.branch(sidT, sidT, "t2-r")
	w.typeInto(b, "b1")
	d := w.branch(sidT, sidT, "t4-r")
	w.typeInto(d, "d1")
	checkVisibleFolded(t, allOf(w.open(sidT)), b)
	checkVisibleFolded(t, allOf(w.open(sidT)), d)

	// B and D each still hang under their turn of the newest T line,
	// starting at their own first turn.
	step := func(t *testing.T, u uiModel, line string) {
		t.Helper()
		u = allOf(u)
		checkHangsUnder(t, u, b, "b1-p", line, "t2-r")
		checkHangsUnder(t, u, d, "d1-p", line, "t4-r")
		checkNoCopies(t, u)
	}

	// 1. squash T's turn 1.
	u := selectRange(t, w.open(sidT), sidT, "t1-p", "t1-r", 0)
	u = drive(t, u, enter)
	t1 := w.replacement(sidT)
	if first := u.m.Rows()[0]; first.Node.SessionID != t1 {
		t.Fatalf("scope did not follow the replacement: %q", screen(u)[0])
	}
	if got := screen(u); !strings.Contains(strings.Join(got, "\n"), "⤶ squashed t1-p..t1-r") {
		t.Errorf("no squash seed:\n%s", strings.Join(got, "\n"))
	}
	checkLines(t, allOf(u), t1, b, d)
	t.Run("after squash", func(t *testing.T) {
		step(t, u, t1)
	})

	// 2. drop T's turn 3.
	u = selectRange(t, u, t1, "t3-p", "t3-r", 2)
	u = drive(t, u, enter)
	t2 := w.replacement(t1)
	if got := rowText(u, t2, "t4-p"); !strings.Contains(got, "✂ 1 turns dropped before this") {
		t.Errorf("row after the drop %q, want the ✂ marker", got)
	}
	checkLines(t, allOf(u), t2, b, d)
	t.Run("after drop", func(t *testing.T) {
		step(t, u, t2)
	})

	// 3. squash T's turn 5: the drop's marker is still owed at t4.
	u = selectRange(t, u, t2, "t5-p", "t5-r", 0)
	u = drive(t, u, enter)
	t3 := w.replacement(t2)
	if w.summaries() != 2 {
		t.Errorf("%d summary calls, want 2", w.summaries())
	}
	t.Run("drop marker survives a later edit", func(t *testing.T) {
		if got := rowText(u, t3, "t4-p"); !strings.Contains(got, "✂ 1 turns dropped before this") {
			t.Errorf("row after the earlier drop %q, want the ✂ marker still there", got)
		}
	})
	checkLines(t, allOf(u), t3, b, d)
	t.Run("after second squash", func(t *testing.T) {
		step(t, u, t3)
	})

	// 4. drop T's turn 4, which D left: D becomes a root from a removed
	// stretch, B stays.
	u = selectRange(t, u, t3, "t4-p", "t4-r", 2)
	u = drive(t, u, enter)
	t4 := w.replacement(t3)
	u = allOf(u)
	checkLines(t, u, t4, b, d)
	root := u.m.Rows()
	var dRow string
	for i, r := range root {
		if r.Node.SessionID == d {
			dRow = screen(u)[i]
			if r.Depth != 0 {
				t.Errorf("D renders at depth %d, want a root", r.Depth)
			}
			break
		}
	}
	if !strings.Contains(dRow, "from a removed stretch") {
		t.Errorf("D's first row %q, want it marked from a removed stretch", dRow)
	}
}

// D. ⏎ on the newest of two in-place replacements hands T's pane over.
func TestScenarioHandoverAfterSeveralEdits(t *testing.T) {
	for _, status := range []string{"idle", "working"} {
		t.Run(status, func(t *testing.T) {
			w := newWorld(t)
			w.trunk(sidT, "t1", "t2", "t3", "t4")
			w.h.panes[sidT] = [2]string{"pane-T", "idle"}

			u := selectRange(t, w.open(sidT), sidT, "t2-p", "t2-r", 0)
			u = drive(t, u, enter)
			t1 := w.replacement(sidT)
			if !strings.HasSuffix(u.status, "⏎ on it to continue there") {
				t.Fatalf("squash status %q", u.status)
			}
			u = selectRange(t, u, t1, "t4-p", "t4-r", 2)
			u = drive(t, u, enter)
			t2 := w.replacement(t1)
			for _, c := range w.h.calls {
				if strings.HasPrefix(c, "close") || strings.HasPrefix(c, "resume") {
					t.Fatalf("an edit opened or closed a pane: %v", w.h.calls)
				}
			}

			t.Run("the cursor lands on the new tip", func(t *testing.T) {
				if n := u.m.Selected(); n == nil || n.SessionID != t2 || !n.IsSessionLeaf {
					t.Errorf("cursor on %s's %s after the edit, want %s's tip", shortID(n.SessionID), n.Node.ID, shortID(t2))
				}
			})

			w.h.panes[sidT] = [2]string{"pane-T", status}
			w.h.calls = nil
			// T was t1..t4: squashing t2 and dropping t4 leaves t3 as the tip.
			u = drive(t, cursorTo(t, u, t2, "t3-r"), enter)
			if status == "working" {
				if u.confirm != "" || !strings.Contains(u.status, "working") {
					t.Fatalf("a working old pane was not refused: status %q confirm %q", u.status, u.confirm)
				}
				for _, c := range w.h.calls {
					if !strings.HasPrefix(c, "live") {
						t.Fatalf("something was resumed or closed: %v", w.h.calls)
					}
				}
				return
			}
			if !strings.Contains(u.confirm, "The pane running the old line is closed; text typed but not sent there is lost.") {
				t.Fatalf("confirmation:\n%s\nstatus %q", u.confirm, u.status)
			}
			u = drive(t, u, enter)
			var acts []string
			for _, c := range w.h.calls {
				if !strings.HasPrefix(c, "live") {
					acts = append(acts, c)
				}
			}
			if got := strings.Join(acts, ", "); got != "resume "+t2+" focus=true, close pane-T" {
				t.Fatalf("herdr saw %q, want the newest line resumed with focus, then T's pane closed", got)
			}
			if u.status != "opened "+shortID(t2)+", old pane closed" {
				t.Fatalf("status %q", u.status)
			}
		})
	}
}

// E. A second overlay, loaded before the first squashed T, saves a label:
// T stays hidden behind its replacement and the label is kept. T is a
// branch, so both overlays hold a record for it — the one the second save
// must not write back without its replaced_by.
func TestScenarioTwoOverlays(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidU, "u1", "u2")
	tb := w.branch(sidU, sidU, "u1-r")
	w.typeInto(tb, "t1", "t2", "t3", "t4")
	u1, u2 := w.open(tb), w.open(tb)

	u1 = selectRange(t, u1, tb, "t2-p", "t3-r", 0)
	u1 = drive(t, u1, enter)
	t1 := w.replacement(tb)

	u2 = cursorTo(t, u2, tb, "t4-p")
	u2 = drive(t, u2, key('L'))
	u2 = drive(t, u2, append(typed("keep"), enter)...)
	if u2.status != "" {
		t.Fatalf("label save: %q", u2.status)
	}

	u := allOf(w.open(tb))
	checkVisibleFolded(t, u, t1)
	checkLines(t, u, sidU, t1)
	checkHangsUnder(t, u, t1, "t1-p", sidU, "u1-r")
	if st, _ := store.Load(w.repo); st.Branches[tb].ReplacedBy != t1 || st.Labels[store.LabelKey(tb, "t4-p")] != "keep" {
		t.Fatalf("store after both saves: T %+v, labels %v", st.Branches[tb], st.Labels)
	}
	t.Run("the label shows on the replacement", func(t *testing.T) {
		if got := rowText(u, t1, "t4-p"); !strings.Contains(got, "★ keep") {
			t.Errorf("t4 in T's replacement renders %q, want the label", got)
		}
	})
}

// §5.3c: a label carried into a replacement is the one last set, so
// clearing it on the replacement clears it, and re-labelling replaces it.
func TestScenarioALabelSetOnAReplacementReplacesTheOlderOne(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "t1", "t2", "t3")
	u := drive(t, cursorTo(t, w.open(sidT), sidT, "t3-p"), key('L'))
	u = drive(t, u, append(typed("keep"), enter)...)
	u = drive(t, selectRange(t, u, sidT, "t1-p", "t1-r", 0), enter)
	t1 := w.replacement(sidT)
	if got := rowText(u, t1, "t3-p"); !strings.Contains(got, "★ keep") {
		t.Fatalf("t3 in the replacement renders %q, want the label carried over", got)
	}

	// Re-labelling first: both steps share the one store, so the order
	// matters, and each needs the label it replaces.
	u = drive(t, cursorTo(t, u, t1, "t3-p"), key('L'))
	u = drive(t, u, append(typed("!"), enter)...)
	if got := rowText(w.open(t1), t1, "t3-p"); !strings.Contains(got, "★ keep!") {
		t.Errorf("after a reload t3 renders %q, want the new label", got)
	}
	st, _ := store.Load(w.repo)
	if _, ok := st.Labels[store.LabelKey(sidT, "t3-p")]; ok || st.Labels[store.LabelKey(t1, "t3-p")] != "keep!" {
		t.Errorf("labels on disk %v, want only the replacement's", st.Labels)
	}

	// Clearing the label on the replacement clears it: T's older copy
	// must not show through.
	u = drive(t, cursorTo(t, u, t1, "t3-p"), key('L'))
	for range "keep!" {
		u = drive(t, u, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	u = drive(t, u, enter)
	if got := rowText(u, t1, "t3-p"); strings.Contains(got, "★") {
		t.Errorf("t3 still renders %q after clearing its label", got)
	}
	if got := rowText(w.open(t1), t1, "t3-p"); strings.Contains(got, "★") {
		t.Errorf("after a reload t3 renders %q, want no label", got)
	}
}

// Two overlays loaded from the same store both show T. The first drops t2;
// the second, still showing the old tree, squashes t3..t4 of T. Nothing is
// lost on disk, but the second edit is spliced from the stale T and its
// replaced_by wins the merge: the first overlay's line becomes an orphan
// root carrying copies of T's turns (BUG 5).
func TestScenarioTwoOverlaysEditTheSameLine(t *testing.T) {
	w := newWorld(t)
	w.trunk(sidT, "t1", "t2", "t3", "t4")
	u1, u2 := w.open(sidT), w.open(sidT)

	u1 = drive(t, selectRange(t, u1, sidT, "t2-p", "t2-r", 2), enter)
	r1 := w.replacement(sidT)
	u2 = drive(t, selectRange(t, u2, sidT, "t3-p", "t4-r", 0), enter)
	// pins BUG 5's current outcome — invert when BUG 5 is fixed
	if !strings.HasPrefix(u2.status, "squashed "+shortID(sidT)+" → ") {
		t.Fatalf("second overlay's squash: %q", u2.status)
	}
	r2 := w.replacement(sidT)

	// What happens today: every transcript stays, the second edit wins T,
	// and the first edit's line is still in the store and on screen.
	for _, sid := range []string{sidT, r1, r2} {
		if _, err := os.Stat(w.path(sid)); err != nil {
			t.Fatalf("%s is gone: %v", shortID(sid), err)
		}
	}
	st, _ := store.Load(w.repo)
	// pins BUG 5's current outcome — invert when BUG 5 is fixed
	if r2 == r1 || st.Branches[r1].Replaces != sidT || st.Branches[r2].Replaces != sidT {
		t.Fatalf("store: T→%s, %s replaces %q, %s replaces %q", shortID(r2),
			shortID(r1), st.Branches[r1].Replaces, shortID(r2), st.Branches[r2].Replaces)
	}
	// pins BUG 5's current outcome — invert when BUG 5 is fixed
	if got := rowText(w.open(sidT), r1, "t3-p"); got != "" {
		t.Fatalf("scoped to T, the first overlay's line shows: %q", got)
	}
	u := allOf(w.open(sidT))
	// pins BUG 5's current outcome — invert when BUG 5 is fixed
	checkLines(t, u, r1, r2)
	// pins BUG 5's current outcome — invert when BUG 5 is fixed
	if got := rowText(u, r2, "t2-p"); got == "" {
		t.Fatalf("the winning line lost t2, which only the other overlay dropped:\n%s", strings.Join(screen(u), "\n"))
	}

	t.Run("the stale overlay's edit does not orphan the other's", func(t *testing.T) {
		t.Skip("BUG 5: a splice from a stale overlay reads the replaced T; its replaced_by wins and the first edit's line is left an unmarked root")
		if r2 != r1 {
			t.Errorf("T resolves to %s, want the first edit's %s kept (the stale edit refused)", shortID(r2), shortID(r1))
		}
		checkNoCopies(t, u)
	})
}
