# herdr-tree — context editing

Date: 2026-09-23
Status: approved; amended 2026-09-23 after the first manual run (§2.2, §2.3,
§2.5, §4, §6): three range options, and no edit ever opens a pane by itself
Scope: v3. Builds on the timeline spec (`2026-09-22-timeline-design.md`),
which stands unchanged except where noted in §9.

---

## 0. Vocabulary

User-visible terms follow git: **squash** replaces a range with its summary,
**drop** removes a range outright, **merge** places a stored summary at a
turn, **branch** starts a new line, and **checkout** moves onto a
replacement. `rebase` is deliberately unused: nothing here replays turns onto
another base.

## 1. The model

v2 could produce a summary and place it, but placing it at a turn always
rewound the line to that turn: everything after it was left behind on the old
line. Compacting a stretch in the middle of a conversation was not possible,
and even compacting "what I just did" needed two steps and a graft point
picked by hand, one turn before the range.

v3 lets the user edit a line's context directly. Select a range, then:

- **squash** — the range is replaced by its summary;
- **drop** — the range is removed.

And `p`, folding a stored summary in at a turn, gains:

- **merge here** — the summary goes in after that turn and everything after
  it is kept.

All three are one operation: **splice**. Keep the line up to its tip, drop a
range of whole turns (possibly empty), optionally put one seeded entry in its
place, and re-attach what followed.

```
before   t1 ── t2 ── t3 ── t4 ── t5 ── t6          ← tip
                      └── range ──┘

squash   t1 ── t2 ── ⤶ squashed t3..t5 ── t6
drop     t1 ── t2 ── t6                              (tree shows ✂ 3 turns dropped)
merge    t1 ── t2 ── ⤶ merged from … ── t3 ── … ── t6  (merge after t2)
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
squash · squash into… · drop · esc back
```

- **squash** — the range is replaced by its summary in its own
  line (a compaction).
- **squash into…** — a move: the range is summarised, the user chooses
  where to merge the summary in, and only then is the range dropped from its
  own line (§2.7).
- **drop** — the range is removed.

**No edit opens a pane.** Every edit only writes; the tree reloads with the
cursor on the result, and moving there is the user's own `⏎` (§6).

### 2.3 One confirmation

Choosing an option opens one dialog that states everything the operation will
do. Nothing asks again afterwards; after `⏎` the operation runs to completion
or stops at the first failure (§6).

- **squash**: v2's cost text ("the model reads this session up
  to the end of the range … that whole prefix is billed"), then:
  `Then: turns <a>–<b> are replaced by the summary · a new session replaces
  this line in the tree (the old one is hidden, kept on disk)`.
- **squash into…**: the same cost text, then `Then: you choose where to merge
  it in. When you do, turns <a>–<b> are dropped from this line.`
- **drop**: `Removes turns <a>–<b>. Costs nothing. No note is left in the
  conversation.` plus the same replacement line.

The turns named are the range **after widening** (§3.1), so the user sees
exactly what goes.

### 2.4 Refusals

Each refusal is a status line with its reason. The range stays fixed so it can
be adjusted.

- The range crosses sessions (v2, unchanged).
- The session's live agent is busy (§6.1). For squash into… this is checked
  when the summary is placed, since that is when the source is written.
- A squash into… whose range covers every turn of its line (it would leave
  nothing); checked before the summary is paid for.
- A drop would remove every turn.
- The range is not on the chain up to the session's tip (§3.3).

### 2.5 `p` — place a summary

After a summary is picked:

- **At the tip of a session open in a live pane**: unchanged from v2 §5 — the
  summary is delivered as a message. No menu.
- **Anywhere else**, a two-option menu:
  - **merge here** — splice with an empty range after this turn, seeded with
    the summary. Replaces the line (§5). Opens nothing.
  - **branch here** — v2's seeded graft: a new line that ends at this turn plus
    the summary. The old line stays visible. Opens nothing (v2 opened a pane;
    the user now presses `⏎` on it).

Merging far back in a long line gives the model a history in which later
turns follow a summary they were written without. That is usually harmless and
is not warned about.

"Merge and remove everything after" is not offered: it is a drop plus a
merge, and `s` → drop covers it.

### 2.6 The summary is still stored

Both summarise options store the summary exactly as v2 does, so `p` can merge
the same summary into another line later.

### 2.7 Fold mode — a move

After **squash into…**'s summary arrives, the overlay stays open in fold
mode, status `summary ready — move to a turn and press ⏎ to merge it in · esc
keeps it for later (p)`.

- `⏎` on a turn does what choosing that summary in `p`'s picker does (§2.5):
  delivered as a message at the live tip, the merge/branch menu elsewhere —
  **and then the range is dropped from its source line**, as a `drop` (§5.1,
  the `✂` marker). The place menu and its confirmation say so:
  `…and turns <a>–<b> are dropped from <source8>`.
- Before anything is written, the source's agent is checked (§6.1); if busy,
  nothing is written anywhere.
- Merging into the source line itself is refused (`merge into another line —
  use squash for this one`).
- **Order: the squash is written first, then the drop.** If the drop fails,
  the status says `squashed into <x>, but the source was not dropped: <err>`
  — a copy, nothing lost. The reverse order could lose the stretch with its
  summary nowhere.
- `esc` leaves fold mode and cancels the move: the source is untouched and the
  summary stays stored for `p` (which never drops).

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

This is the only rule that keeps the result valid. A mid-turn drop leaves a
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
   - **squash / merge**: to the seed. The seed is one user entry carrying
     the seed text verbatim, parented to the last entry before the range.
   - **drop**: directly to the last entry before the range.
   - If nothing precedes the range but a preamble, "the last entry before the
     range" is the preamble's last entry; if there is no preamble either, it
     is null and the seed or the next prompt becomes the root.
5. If nothing follows the range, the seed (or, for a drop, the last entry
   before the range) is the new leaf.
6. `sessionId`/`session_id` and `cwd` are rewritten as in `GraftSeeded`.
   **Entry uuids are kept.** Branch re-attachment (§5.3) depends on it.

Seeds: squash uses `CompactionPrefix` (`⤶ squashed <from>..<to>`); merge
always uses `SummaryPrefix` (`⤶ merged from <session>`) — nothing was
removed, so nothing contracted (§6b). The existing `ErrUnmarkedSeed` check
applies.

### 3.3 Refused, nothing written

- The transcript has unparseable lines (`ErrPartialTranscript`) or an
  unsupported version (`checkVersion`).
- The range's start or end is not in `Select(es, tip)`: the range lies on a
  stretch the session has already rewound away from.
- A drop whose widened range covers every turn. (A squash of every turn is
  allowed; the result is the preamble plus the summary.)

### 3.4 Native `/compact`

Claude Code's own `/compact` writes a `compact_boundary` where the parent chain
restarts. Splice follows `parentUuid` as graft does, so it sees only the line
after the last boundary — the same line the tree shows.

## 4. Squash, end to end

1. Confirm (§2.3), with the busy check (§6.1).
2. `adapter.Summarise` over the widened range — unchanged from v2, billed.
3. Store the summary (§2.6).
4. Busy re-check (§6.1, step 3).
5. Splice with the compaction seed, save (§5), reload the tree with the cursor
   on the new line's tip. Nothing is opened (§6).

If the summary call fails, nothing is written or hidden.

squash into… is steps 2–3 (the whole-line refusal instead of the busy
check), then fold mode (§2.7), where the placement writes the squash and then
drops the source.

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

- **Drop**: the row after the drop shows `✂ <n> turns dropped` in the muted
  style. It comes from the store record; nothing about the drop is in the
  transcript.
- **Squash / merge**: the seeded entry renders blue or orange through its
  `⤶` prefix, per v2 §6b. Unchanged.

## 6. Live handover

An edit never opens or closes a pane. The handover happens when the user moves.

### 6.1 The edit

1. **At confirm**: resolve the session's live agent and its `agent_status`
   (`parseAgentList` keeps it alongside the pane id). If `working`, refuse
   (§2.4); nothing is written.
2. (continue only) Summarise.
3. **Re-check `agent_status` immediately before splicing.** If `working`: write
   nothing, status `summary stored — agent is busy; select again or use p`.
4. Splice, save the store, reload with the cursor on the new line's tip.

A pane that was running the old line keeps running it. Anything typed there
lands on the hidden line; the status says `⏎ on it to continue there`.

### 6.2 `⏎` on a replacement

`⏎` on the tip of a line walks its `replaces` chain (a line edited several
times without moving) for a session still open in a pane.

- None found: plain resume, as today.
- Found, and its agent is `working`: refused — closing it would kill the
  running turn.
- Found: confirm `Check out the new line. The pane running the old line is
  closed; text typed but not sent there is lost.` Then open the new session
  **with focus**, and close the old pane only if the open succeeded. If the
  open failed, the old pane is left running.

`herdr pane close` has no guard of its own; the status check is the guard.
Branching and plain resume keep `--no-focus`.

### 6.3 Scope follows the replacement

The tree's scope and trunk are computed from `Resolve(current)`, so editing the
session you are in keeps showing its line. Sending a message (the live-tip
fold) still targets only the agent actually running, never a resolved session.

### 6.4 Status lines

Each says what did happen:

- `squashed 1a2b3c4d → 5e6f7a8b — ⏎ on it to continue there`
- `dropped 8 turns from 1a2b3c4d → 5e6f7a8b — ⏎ on it to continue there`
- `opened 5e6f7a8b, but the old pane did not close: <err>`
- `could not open 5e6f7a8b — old pane left running: <err>`

Errors are passed through `scrubbed` as today; no message content reaches a
status line.

## 7. Cost

- Drop and merge spend nothing: they are local file writes.
- Squash spends one summary call, as v2's summarise does.
- The first message in a new session is sent with a cold prompt cache: the
  whole (now smaller) context is billed uncached once.

The summary call is `claude -p` with the inherited environment. Per Claude
Code's documentation it draws on a claude.ai subscription's limits unless an
API key, auth token, `apiKeyHelper` or cloud provider is configured. herdr-tree
adds no credential of its own.

## 8. Testing

- Unit tests, no API calls:
  - `Splice`: middle range; range at turn 1 with and without a preamble; drop
    vs. squash vs. merge (empty range); nothing after the range; range off
    the tip chain (refused); whole-session drop (refused); turns appended after
    the range was fixed.
  - Widening to whole turns, including ranges that start and end on tool
    calls.
  - `replaced_by` resolution through two splices; branch re-attachment,
    including a branch off a removed turn.
  - The drop marker; the range menu; the `p` merge/branch menu.
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
- §5: fold-back away from a live tip offers merge here beside branch here.
- §6b: "blue means this line contracted" becomes literally true for squash.
- The standing rule "never delete a session the user could still resume"
  stands. A replaced session is hidden, not deleted.

## 10. Open questions

1. Does Claude Code resume a spliced file without complaint? The structure is
   the same one it writes, but a spliced file has been validated only by our
   own checks until the manual run in §8.
2. Where does Herdr put focus when `pane close` closes a pane that did not
   have focus? The new pane is opened with focus first, so this should not
   matter; to be observed in the manual run.
