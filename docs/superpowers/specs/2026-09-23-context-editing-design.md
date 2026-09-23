# herdr-tree — context editing

Date: 2026-09-23
Status: draft for review
Scope: v3. Builds on the timeline spec (`2026-09-22-timeline-design.md`),
which stands unchanged except where noted in §9.

---

## 1. The model

v2 could produce a summary and place it, but placing it at a turn always
rewound the line to that turn: everything after it was left behind on the old
line. Compacting a stretch in the middle of a conversation was not possible,
and even compacting "what I just did" needed two steps and a graft point
picked by hand, one turn before the range.

v3 lets the user edit a line's context directly. Select a range, then:

- **summarise & compact** — the range is replaced by its summary;
- **cut** — the range is removed.

And `p`, folding a stored summary in at a turn, gains:

- **insert here** — the summary goes in after that turn and everything after
  it is kept.

All three are one operation: **splice**. Keep the line up to its tip, drop a
range of whole turns (possibly empty), optionally put one seeded entry in its
place, and re-attach what followed.

```
before   t1 ── t2 ── t3 ── t4 ── t5 ── t6          ← tip
                      └── range ──┘

compact  t1 ── t2 ── ⤶ compacted t3..t5 ── t6
cut      t1 ── t2 ── t6                              (tree shows ✂ 3 turns cut)
insert   t1 ── t2 ── ⤶ summary of … ── t3 ── … ── t6  (insert after t2)
```

A splice writes a **new session** and **replaces** the old line with it: the
old session is hidden from the tree and its file is kept on disk (§5). This is
different from branching, which leaves both lines visible.

## 2. Interaction

### 2.1 Selecting

`s` fixes the range's end at the cursor; the user moves to the start and
presses `s` or `⏎`. `esc` cancels. Unchanged from v2 §4, except that the
footer reads `s select` rather than `s summarise`.

### 2.2 The range menu

With a range fixed, `⏎` opens a menu over it:

```
summarise & compact · cut · esc back
```

Only those two options exist. The menu is the place later range operations
go; none is designed here.

### 2.3 One confirmation

Choosing an option opens one dialog that states everything the operation will
do. Nothing asks again afterwards; after `⏎` the operation runs to completion
or stops at the first failure (§6).

- **summarise & compact**: v2's cost text ("the model reads this session up to
  the end of the range … that whole prefix is billed"), then:
  `Then: turns <a>–<b> are replaced by the summary · a new session replaces
  this line in the tree (the old one is hidden, kept on disk)`.
- **cut**: `Removes turns <a>–<b>. Costs nothing. No note is left in the
  conversation.` plus the same replacement line.
- If the session is open in a live pane, both add: `The pane running this
  session is closed and the new one opens. Text typed but not sent in that
  pane is lost.`

The turns named are the range **after widening** (§3.1), so the user sees
exactly what goes.

### 2.4 Refusals

Each refusal is a status line with its reason. The range stays fixed so it can
be adjusted.

- The range crosses sessions (v2, unchanged).
- The session's live agent is `working` (§6.1).
- A cut would remove every turn.
- The range is not on the chain up to the session's tip (§3.3).

### 2.5 `p` — fold back

After a summary is picked:

- **At the tip of a session open in a live pane**: unchanged from v2 §5 — the
  summary is delivered as a message. No menu.
- **Anywhere else**, a two-option menu:
  - **insert here** — splice with an empty range after this turn, seeded with
    the summary. Replaces the line (§5).
  - **branch here** — v2's seeded graft: a new line that ends at this turn plus
    the summary. The old line stays visible. Unchanged.

Inserting far back in a long line gives the model a history in which later
turns follow a summary they were written without. That is usually harmless and
is not warned about.

"Insert and remove everything after" is not offered: it is a cut plus an
insert, and `s` → cut covers it.

### 2.6 The summary is still stored

summarise & compact stores its summary exactly as v2 does, so `p` can fold the
same summary into another line later. Summarising for later use elsewhere
stays possible.

## 3. The splice

`claude.Splice`, next to `GraftSeeded` in `internal/claude/graft.go`.
`GraftSeeded` is not changed; branching and v2's fold-back keep using it.

### 3.1 Whole turns only

Tree rows are entries — prompts, replies, tool calls — so a selected range can
start or end mid-turn. A **turn** runs from an entry the user typed (a
`KindHuman` prompt, or an injected `⤶` entry) up to, not including, the next
one. Entries before the first prompt are the session's **preamble** and belong
to no turn.

The range is widened outward to whole turns: its start moves back to its
turn's prompt, its end forward to the last entry before the next prompt.

This is the only rule that keeps the result valid. A mid-turn cut leaves a
tool_use without its tool_result, which the Messages API rejects, or puts two
user or two assistant messages side by side. Turn boundaries avoid both.
Measured on 112 real transcripts (973 boundaries): all 9146 tool pairs lie
inside one turn, and the only uuid reference crossing a boundary is each
prompt's `parentUuid` to the previous turn.

### 3.2 What is written

A new session file, mode 0600, in the project directory for the session's cwd.
The source transcript is never modified.

1. Parse the source **fresh** at splice time (turns appended since the range
   was fixed are kept, after the splice point).
2. Keep: `Select(es, tip)` — the ancestor chain of the session's tip under
   Select's four rules. Uuid-less bookkeeping is dropped as in `GraftSeeded`.
3. Drop: every kept entry whose turn lies in the widened range. An entry's turn
   is its chain position's turn; off-chain entries kept by rules 2–4 take the
   turn of the entry that caused them to be kept.
4. Re-attach the first entry after the range (the next turn's prompt):
   - **compact / insert**: to the seed. The seed is one user entry carrying
     the seed text verbatim, parented to the last entry before the range.
   - **cut**: directly to the last entry before the range.
   - If nothing precedes the range but a preamble, "the last entry before the
     range" is the preamble's last entry; if there is no preamble either, it
     is null and the seed or the next prompt becomes the root.
5. If nothing follows the range, the seed (or, for a cut, the last entry
   before the range) is the new leaf.
6. `sessionId`/`session_id` and `cwd` are rewritten as in `GraftSeeded`.
   **Entry uuids are kept.** Branch re-attachment (§5.3) depends on it.

Seeds: compact uses `CompactionPrefix` (`⤶ compacted <from>..<to>`), insert
uses `SummaryPrefix` or `CompactionPrefix` by v2's `foldBackSeed` rule. The
existing `ErrUnmarkedSeed` check applies.

### 3.3 Refused, nothing written

- The transcript has unparseable lines (`ErrPartialTranscript`) or an
  unsupported version (`checkVersion`).
- The range's start or end is not in `Select(es, tip)`: the range lies on a
  stretch the session has already rewound away from.
- A cut whose widened range covers every turn. (A compaction of every turn is
  allowed; the result is the preamble plus the summary.)

### 3.4 Native `/compact`

Claude Code's own `/compact` writes a `compact_boundary` where the parent chain
restarts. Splice follows `parentUuid` as graft does, so it sees only the line
after the last boundary — the same line the tree shows.

## 4. Summarise & compact, end to end

1. Confirm (§2.3), with the busy check (§6.1).
2. `adapter.Summarise` over the widened range — unchanged from v2, billed.
3. Store the summary (§2.6).
4. Busy re-check (§6.1, step 3).
5. Splice with the compaction seed, then §5 and §6.

If the summary call fails, nothing is written, hidden or closed.

## 5. Store and tree

### 5.1 Store

`store.Branch` gains optional fields; no migration.

- `replaced_by` (session id) — set on the **old** session's record. If the old
  session has no record, one is created carrying only this field.
- `kind` — on the **new** session's record: `compacted`, `cut` or `inserted`.
  Absent means a v2 branch.
- `replaces` — on the new record: the old session's id.
- `cut` — for `kind: cut`, the number of turns removed and the uuid of the
  first entry after the cut (where the marker renders); empty when nothing
  follows, and the marker then renders on the line's last row.

The new record's `grafted_from` is a **copy of the old record's**, not a
pointer at the old session: the replacement takes the old line's place,
including where it hung. A compacted branch stays under the trunk turn it
left; a compacted root stays a root.

`replaced_by` is one-way. `Save`'s merge never lets a record without it
overwrite one on disk that has it, so a second overlay saving an unrelated
change cannot un-hide a replaced line.

A session is hidden only if the session it resolves to is present. If the
replacement's file is gone, the old line shows again rather than vanishing.

### 5.2 Hiding

A session whose record has `replaced_by` is not rendered. `Discover` still
finds its file; nothing is deleted. The replacement renders from its first
turn in the old line's place, because its record carries the old line's
`grafted_from` (§5.1).

No "show hidden" toggle is built.

### 5.3 Re-attaching branches

When a branch's `grafted_from` names a hidden session, the lookup follows
`replaced_by` — repeatedly, through any number of splices — and attaches to the
node with the same uuid in the newest line. If that node was compacted or cut
away, the branch renders as a root line marked `from a removed stretch`.

Stored edges are never rewritten; resolution happens when the tree is built.

### 5.4 Markers

- **Cut**: the row after the cut shows `✂ <n> turns cut` in the muted style.
  It comes from the store record; nothing about the cut is in the transcript.
- **Compact / insert**: the seeded entry renders blue or orange through its
  `⤶` prefix, per v2 §6b. Unchanged.

## 6. Live handover

Only when a live Herdr pane is running the session being spliced.

### 6.1 Order

Each step runs only if the one before succeeded.

1. **At confirm**: resolve the session's live agent and its `agent_status`.
   `parseAgentList` keeps `agent_status` alongside the pane id. If `working`,
   refuse (§2.4); nothing is written.
2. (compact only) Summarise.
3. **Re-check `agent_status` immediately before splicing.** The user may have
   typed into the old pane during the summary call. If `working`: write
   nothing, status `summary stored — agent is busy; select again or use p`.
4. Splice.
5. Save the store (`replaced_by`, new record). From here the tree is correct
   whatever fails next.
6. Open the new session: `Resume`, with the split made **with focus** (no
   `--no-focus`) for this path only. Branching and plain resume keep
   `--no-focus`.
7. Close the old pane: `herdr pane close <pane_id>` — only if step 6
   succeeded. If step 6 failed, the old pane is left running, so the user is
   never left with no pane.

`herdr pane close` has no guard of its own; step 1 and step 3 are the guard.
Text typed but not sent in the old pane cannot be detected and is lost; the
confirmation says so (§2.3).

### 6.2 Not live

A session with no live pane is spliced and the store saved (steps 4–5). Nothing
is opened; the user presses `⏎` on the new line when they want to continue it.

### 6.3 Status lines

Each says what did happen, following v2's fold-back:

- `compacted into <id> — opened in a new pane, old pane closed`
- `compacted into <id>, but it did not open — old pane left running`
- `compacted into <id> and opened, but the old pane did not close: <err>`
- `cut 8 turns from <id> → <new id>` (not live)

Errors are passed through `scrubbed` as today; no message content reaches a
status line.

## 7. Cost

- Cut and insert spend nothing: they are local file writes.
- Compaction spends one summary call, as v2's summarise does.
- The first message in a new session is sent with a cold prompt cache: the
  whole (now smaller) context is billed uncached once.

The summary call is `claude -p` with the inherited environment. Per Claude
Code's documentation it draws on a claude.ai subscription's limits unless an
API key, auth token, `apiKeyHelper` or cloud provider is configured. herdr-tree
adds no credential of its own.

## 8. Testing

- Unit tests, no API calls:
  - `Splice`: middle range; range at turn 1 with and without a preamble; cut
    vs. compact vs. insert (empty range); nothing after the range; range off
    the tip chain (refused); whole-session cut (refused); turns appended after
    the range was fixed.
  - Widening to whole turns, including ranges that start and end on tool
    calls.
  - `replaced_by` resolution through two splices; branch re-attachment,
    including a branch off a removed turn.
  - The cut marker; the range menu; the `p` insert/branch menu.
- Fixtures copied from real transcript shapes — parallel tool calls,
  attachments, a preamble, a `compact_boundary` — anonymised. A fixture that
  cannot occur in reality is how six v2 tests came to be unable to fail.
- Every herdr call goes through `stubHerdr` on `PATH`, recording argv. The
  handover's step order and every failure branch are tested this way,
  including "resume fails → close is never called".
- Mutation check on the risky logic — the re-parent, the widening, the busy
  re-check, close-after-open ordering: each mutated to a live-but-wrong
  expression, and a test must go red.
- Real-transcript check: a read-only script that splices every possible range
  of a copy of each local transcript into a temp dir and checks every tool_use
  has its tool_result and the chain is unbroken.
- **Not automated**: resuming a spliced session with `claude`. It spends
  budget, needs explicit approval, and is the one proof that Claude Code
  accepts a spliced file. To be run once before merging.

## 9. Changes to the timeline spec

- §4: `s` selects a range; summarising is one option of the range menu.
- §5: fold-back away from a live tip offers insert here beside branch here.
- §6b: "blue means this line contracted" becomes literally true for compaction.
- The standing rule "never delete a session the user could still resume"
  stands. A replaced session is hidden, not deleted.

## 10. Open questions

1. Does Claude Code resume a spliced file without complaint? The structure is
   the same one it writes, but a spliced file has been validated only by our
   own checks until the manual run in §8.
2. Where does Herdr put focus when `pane close` closes a pane that did not
   have focus? The new pane is opened with focus first, so this should not
   matter; to be observed in the manual run.
