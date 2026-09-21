# herdr-tree — design

Date: 2026-09-21
Status: approved for implementation planning
Scope: v1 thin slice (tree view + branch). Summarize and artifacts deferred to v1.1.

Supersedes the ambiguous parts of the original concept document. Where this
document and the concept document disagree, this one is current; section
references like §5 point at the concept document.

---

## 1. What this is

A Herdr plugin for navigating and branching Claude Code conversations in a
repository. It draws a tree of conversation turns, and it can start a new
Claude session that continues from any turn in that tree.

It is not an agent harness, not a Git tool, and not a second copy of your
conversations. It owns exactly one piece of data that exists nowhere else:
the edge recording that session B was branched from turn N of session A.

## 2. Verified before designing

A spike on 2026-09-21 established the following against Claude Code 2.1.278.
These are measurements, not assumptions, and the design depends on them.

**Claude Code resumes a hand-written transcript.** Session lookup scans
`~/.claude/projects/<cwd-slug>/*.jsonl` and happens *before* authentication.
A synthesized file containing only the ancestor chain of a chosen node,
written under a fresh uuid, resumed correctly: the model recalled the two
kept turns and none of the three pruned ones.

**Repointing the leaf is not enough.** Copying a transcript intact and
setting a trailing `last-prompt.leafUuid` to an earlier node did *not*
truncate history — the resumed session replayed all five turns. History is
reconstructed from the entries present in the file. Branching therefore
requires physically writing the pruned chain.

**`-p --resume` appends to the target transcript.** A probe grew a 20-entry
session to 59 entries. `--fork-session` avoids this: verified to leave the
source byte-identical while writing a separate fork file.

**Resume injects a synthetic turn.** Claude Code writes a
`"Continue from where you left off."` user entry on resume. It is
structurally identical to a real prompt and must be filtered, or every
resumed branch grows a junk node.

**There are no naturally occurring conversation forks.** Across 33 projects
and 109 session files on this machine, genuine user-prompt divergence: zero.
An initial scan reporting 50 files with forks was wrong — parallel tool calls
and multi-block assistant responses share a `parentUuid` and imitate
branching. Consequence: the tree of §5 is not latent in Claude Code's data.
herdr-tree creates it.

**Herdr's pane→session association is unreliable.** `pane.agent_session.value`
returns the last Claude session id observed in the pane, which a headless
`-p` run silently overwrites; this was observed live during the spike.
`~/.claude/sessions/<pid>.json` carries `kind: "interactive"` and is
authoritative.

**Scale is a non-issue.** 109 session files discovered, parsed for `cwd`, and
grouped by git common dir in 0.03s. No index, cache, or daemon is needed.

## 3. Decisions

| Question | Decision |
|---|---|
| What is a tree node? | One conversation turn (user prompt) |
| What is a branch? | One Claude session — a path of turns |
| Tree scope | Repo-wide, git worktrees folded into one tree |
| Metadata location | `$HERDR_PLUGIN_CONFIG_DIR/<hash>/tree.json`, private |
| Artifact location | `<repo>/.herdr-tree/artifacts/`, committable (v1.1) |
| Summary generation | `claude -p --resume --fork-session`, discard fork (v1.1) |
| Runtime | Go + Bubble Tea, single static binary |
| v1 scope | Tree view + branch. Nothing else. |

Rationale for the thin slice: Summarize and Create-artifact amount to running
a templated prompt and saving stdout — real value, but you can get it today by
typing the prompt yourself. Tree + graft cannot be done by hand at all.
Shipping it alone answers whether the tree-shaped workflow fits how you work,
which is what §25 says the MVP is for. The `artifacts[]` field ships in the
schema from day one, so v1.1 adds behavior rather than a migration.

## 4. Conceptual model

Branch and session are the same thing. §13's three concepts collapse to two.

```
per-session turn chains   (parsed from transcripts, read-only)
+ graft edges             (tree.json — the only data we own)
= the tree
```

A turn is identified by `(session_id, entry_uuid)`, stable across runs.

## 5. Discovery

```
focused pane cwd
  → git rev-parse --path-format=absolute --git-common-dir → repo root
    (not a repo → scope to the literal cwd, and say so in the header)
  → scan ~/.claude/projects/*/*.jsonl, read each file until the first entry carrying `cwd` (within ~40 lines)
  → keep sessions whose cwd resolves to the same repo root
```

Worktrees fold in automatically: a session in
`dome-test-1-session-frame-loss-above-400hz` resolves to the `dome-test-1`
root and appears in that one tree, tagged with its worktree.

Turn extraction, per session:

```
type == "user"
  AND NOT isSidechain
  AND "toolUseResult" not in entry
  AND content is not a tool_result block
  AND text != "Continue from where you left off."
```

No session filtering. Dead and one-turn sessions render as single leaves and
sort to the bottom by mtime. A filter that guesses wrong hides real work.

## 6. Store

`$HERDR_PLUGIN_CONFIG_DIR/<sha256(repo_root)[:12]>/tree.json`, where that
variable is set by Herdr when it invokes the plugin and falls back to
`herdr plugin config-dir herdr-tree`
(`~/.config/herdr/plugins/config/herdr-tree/`):

```json
{
  "version": 1,
  "repo_root": "/home/somliga/projects/surtr",
  "branches": {
    "f2af34a4-...": {
      "grafted_from": { "session_id": "82cb69f2-...", "node": "ae6b7671-..." },
      "title": "Session-based auth",
      "created_at": "2026-09-21T17:40:00Z",
      "artifacts": []
    }
  }
}
```

Branches are keyed by session id. There is no separate alias scheme: the
session id is already unique and stable, the first 8 characters are what the
TUI shows, and in v1.1 they name the artifact directory. Only grafted
sessions have an entry here — a session discovered from a transcript but
never branched has no store record at all, and needs none.

`Save` merges on write rather than overwriting: two panes can each branch and
both edges survive. v1 never deletes a branch, so a merge cannot resurrect
something intentionally removed.

Private, not in the repo: one tree.json is shared by all worktrees of a repo,
so it never conflicts and never needs gitignoring. Artifacts go in the repo
(v1.1) because those are yours to review and commit.

Titles are computed, not stored, unless overridden — first non-blank line of
the turn, truncated. Claude Code already writes `ai-title` entries per
session, so session labels come free. `version` exists so a format break is
detectable rather than silently misparsed.

## 7. Adapter boundary

Neutral types cross the boundary. JSONL never does.

```go
type Node struct { ID, Title string; At time.Time }

type Session struct {
    ID, CWD, Title string
    Updated        time.Time
    Nodes          []Node    // ordered root → leaf
    Live           bool      // a process currently holds it, per the session registry
}

// Pane is what Herdr reports about the invoking pane: id, cwd, and the
// agent_session value it believes is running there.
type Pane struct { ID, CWD, Agent, AgentSessionID string }

type Adapter interface {
    Name() string
    Discover(repoRoot string) ([]Session, error)
    Current(pane Pane) (sessionID string, err error)
    Branch(src Session, atNode, dstCWD string) (newSessionID string, err error)
    Resume(sessionID, cwd string) error
}
```

§11's `create_context()` is deferred with the artifact work. `Preview()` is
not in §11 but is required by the confirm dialog below, which must state
what a graft carries before it is written; it computes without writing.
`Name()` is bookkeeping for the adapter registry.

**Current()** reads `~/.claude/sessions/*.json`, keeps `kind == "interactive"`
entries whose `cwd` matches the pane, and uses Herdr's `agent_session.value`
only to disambiguate multiple matches. Disagreement resolves to *unknown*,
never to a guess — branching from the wrong session silently destroys work.

**Branch()** — the graft:

1. Walk `parentUuid` from the chosen node to the root; keep that chain.
2. Additionally keep assistant entries sharing a kept entry's `requestId`
   (one response is written as many entries).
3. Additionally keep `attachment` children of kept nodes.
4. Rewrite `sessionId`, `session_id`, and `cwd`.
5. Drop `last-prompt`, `ai-title`, `cost-state`.
6. Append one `last-prompt` with `leafUuid` at the chosen node.
7. Write `<new-uuid>.jsonl`, mode 0600, tmp-then-rename, into the destination
   cwd's project slug directory.

On step 6: the working graft included this entry, so it is not proven
unnecessary; the repoint experiment proved it is not sufficient alone.
Include it because it matches what Claude Code writes. Do not rely on it.

**Resume()** delegates and never spawns a process itself:

```
herdr pane split --current --direction right --cwd <dst> --no-focus
  → read .result.pane.pane_id
herdr agent start <name> --kind claude --pane <id> -- --resume <sid>
```

The TUI overlay itself is opened by Herdr, not by us:

```
herdr plugin pane open --plugin herdr-tree --entrypoint tree \
  --placement overlay --cwd <repo root>
```

The `herdr` binary is named by `$HERDR_BIN_PATH`, falling back to `herdr`
on PATH.

Herdr owns process lifecycle; herdr-tree owns files. A future Codex adapter
reuses this half unchanged.

**Format-break detection** belongs to the adapter. It records the `version`
Claude Code stamps on entries and refuses to graft, legibly, when the shape
stops matching. A wrong graft yields a plausible session with the wrong
history, which is worse than a refusal.

**Constraint inherited by v1.1:** any `claude -p --fork-session` run for
Summarize will clobber Herdr's pane→session association, exactly as the spike
did. v1 runs no Claude process during a graft and is immune. v1.1 is not.

## 8. TUI

One overlay pane — a repo-wide forest does not fit a fixed popup; one repo
here has 53 sessions.

```
┌─ herdr-tree · surtr ─────────────────────────────── 22 branches ─┐
│                                                                  │
│  82cb69f2  hello, your on a dome session, what have been dis…    │
│  └─ i want to discuss about the weather                          │
│     ├─ what do you think                                         │
│     │  └─ please think more open                                 │
│     │     └─ good conclusion, can you push it to…   ● current    │
│     │                                                            │
│     └─ ↳ f2af34a4  Session-based auth                   grafted  │
│        └─ Redis-backed sessions                                  │
│                                                                  │
│  a4f1c802  Analyze the authentication system and propose a r…    │
│  ▸ 4 turns                                            collapsed  │
│                                                                  │
│  ⚠ 7e213e01  transcript unreadable — metadata only               │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│ ↑↓ move  ←→ fold  ⏎ open  b branch  esc close                    │
└──────────────────────────────────────────────────────────────────┘
```

A graft edge is the only thing that indents a new subtree. Alias and short id
are right-aligned so structure stays readable with long titles. Sessions sort
by mtime descending.

Two actions, matching §8's Continue and Branch. `⏎` resumes the selected
node's session — no files written. `b` grafts at the selected node, then
resumes the result.

`b` confirms first, which is where §24's "show what is being sent" is paid:

```
Branch from:  "what do you think"
Carries:      3 turns · 12 entries · 41 KB
New session:  <uuid> in ~/projects/surtr
Opens:        split right, unfocused

[enter] branch   [esc] cancel
```

Bytes, not tokens: bytes are true, tokens would be a guess.

In-TUI keys are hardcoded in v1. The Herdr-level shortcut is already
configurable through the user's own `config.toml`, so §21 is satisfied
without any config code of ours. A second keymap layer for six keys is config
for a value that will not change.

```toml
[[keys.command]]
key = "prefix+t"                 # prefix+t is unbound in Herdr defaults
type = "plugin_action"
command = "herdr-tree.open"
```

States that must render rather than crash: not a git repo; no sessions found;
transcript unparseable; graft edge whose child file is gone.

## 9. Failure handling

| Failure | Behavior |
|---|---|
| Not in a git repo | Scope to literal cwd, say so in the header |
| Transcript unparseable / format changed | `⚠` row; branch refused on that node; rest of tree fine |
| Source session file deleted | Graft edge becomes a tombstone; children still render |
| `herdr agent start` fails after graft | Orphan session stays on disk, appears as a root. Not deleted — it is valid user data |
| Two panes writing tree.json | Save re-reads and merges, then atomic tmp+rename. A plain last-writer-wins would silently discard a branch the other pane just created |
| tree.json corrupt | Back up, start fresh. Only graft edges lost |
| Pane runs a non-Claude agent | "No adapter for codex" |

Grafts write tmp-then-rename, so a crash cannot leave Claude Code a
half-parsed session.

## 10. Security

v1 makes no network calls. Grafting is a file copy; resuming delegates to
Herdr, which launches Claude under the user's own auth.

Transcripts are mode 0600. **Grafted files must be written 0600 explicitly**,
not left to umask, or branching quietly downgrades conversation content to
world-readable.

Branching duplicates whatever the conversation contains — including any
credential that leaked into it — into a second file that is easy to forget.
The confirm dialog naming turns and bytes is the mitigation.

Logs carry ids and counts, never message content.

## 11. Verification

**Golden test for the graft.** Fixture transcript in, expected kept-uuid set
out. All three non-obvious rules live here — `requestId` siblings,
`attachment` children, and the synthetic-turn filter — and each cost a cycle
to discover. This is the only logic in the project that can be wrong in a way
that looks right.

**One end-to-end script**, reproducing the spike: graft a fixture at a known
node, `claude -p --resume`, assert the reply mentions the kept topic and not
the pruned one. Costs a few cents per run. Manual, not CI. It is the only
thing that catches Claude Code changing its format.

## 12. Out of scope

Git operations of any kind (§20). The only git invocation in the design is
`rev-parse --git-common-dir`, for scoping.

Deferred to v1.1, in order: Summarize, Create artifact, Open artifact,
Restart from artifact. The store and node schema already accommodate them.

Deferred indefinitely: Codex and Pi adapters, cross-agent handoff, branch
comparison, search, automatic summaries, token estimates, archive,
export/import.

## 13. Known fragility

The transcript format is undocumented and verified against Claude Code
2.1.278 only. It will break. That risk is confined to the Claude adapter by
design, is detected rather than absorbed (§7), and is accepted because
branch-from-an-earlier-point is the one capability in the problem statement
that nothing else provides.
