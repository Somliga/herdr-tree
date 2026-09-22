# herdr-tree — timeline model

Date: 2026-09-22
Status: draft for review
Scope: v2. Builds on the v1 spec (`2026-09-21-herdr-tree-design.md`), which
stands unchanged except where noted in §9.

---

## 1. The model

A repository's conversations form a **trunk** and its **branches**.

The trunk is one linear timeline. You move along it, and you can rewind it: go
back to an earlier turn and carry on from there. What you had done after that
turn is not destroyed — it becomes a branch hanging off the point you rewound
to.

A branch is itself a timeline and rewinds the same way. Nothing is special
about the trunk except that it is where you are.

The interesting move is the way back. You do not merge a branch's conversation
into the trunk — that would just be a longer conversation. You **summarise**
the branch, then choose a point on the trunk and append the summary there. The
knowledge rejoins the trunk without the transcript.

```
trunk    t1 ── t2 ── t3 ── t4 ── t5          ← where you are
                  │
branch            └── b1 ── b2 ── b3         ← explored, then summarised
                                    │
                                    └─ summary appended at t4,
                                       which rewinds the trunk to t4
                                       and seeds it with what b learned
```

## 2. The trunk is inferred, never marked

The trunk is the lineage of the session you are currently in: take the live
session, walk back through graft edges to a root, and that path is the trunk.

Nothing is stored and nothing is marked. Rewinding puts you in the new session,
so the trunk follows you automatically — which is the property that makes this
model cheap. The alternative, an explicit "main" pointer, would need
maintaining, could disagree with where you actually are, and would be one more
thing to get wrong.

Consequences worth stating:

- **The trunk changes when you move.** Opening an old branch makes it the trunk
  and the former trunk a branch. That is correct: the trunk is "the line I am
  working on", not "the important one".
- **With no live session there is no trunk.** The tree falls back to the v1
  presentation, every session a root. `Current()` already returns an error
  rather than guessing, and that error is the signal.
- **Two panes can disagree.** Each infers from its own session. They are both
  right; the tree is a view, not a shared truth.

## 3. Rewinding

`⏎` on a turn continues from it (v1, Task 18): resume in place if it is the
tip, graft otherwise. Under this model a graft is a **rewind**, and the only
change is presentation:

- the trunk path renders as the main line, at depth 0
- a session that diverges from the trunk renders as a branch, indented once at
  the turn it diverged from
- the turns after the rewind point in the former trunk are exactly such a
  divergence, so they appear as a branch with no special handling

The abandoned tail is never deleted, moved or rewritten. It is a session on
disk and stays one. This is the v1 rule — *never delete a session the user
could still resume* — and it is what makes rewinding safe to do casually.

## 4. Summarising a branch

On demand, not automatically. `s` on any session's row summarises that session.

Mechanically: `claude -p --resume <sid> --fork-session`, capture stdout, delete
the fork. Verified in v1: `--fork-session` leaves the source byte-identical and
writes a separate file we then remove. The summary text is stored in
`tree.json` against that session.

On demand rather than at branch time because a summary costs a real API call
and most branches are never folded back. A branch you abandon and forget should
cost nothing.

The prompt is a template and configurable later. It should ask for: what was
being attempted, what was decided, what was rejected and why, what was left
unfinished. The "why not" matters most — it is the part that stops the trunk
re-exploring the same dead end.

## 5. Appending a summary

Select a turn on the trunk, choose a summarised branch, confirm.

This is **the same operation as rewinding**, with one addition: the grafted
transcript gets one extra entry appended — a user turn carrying the summary —
before it is resumed. So "append a summary at t4" is "rewind to t4, seeded".

Verified against real Claude Code on 2026-09-22: a transcript grafted at a
chosen turn with an injected user entry resumes with BOTH the grafted history
and the injected text present. The model answered a question that required each.

That the two operations are the same mechanism is the strongest evidence the
model is right, and it means §5 is mostly UI over §3.

## 6. How an injected summary appears

It is not something you typed, and the tree must not pretend otherwise.

The injected entry is marked two ways: a visible prefix in its text, and a
`herdrTree` object on the entry recording `{kind: "summary", from: <branch id>}`.
The field is for us, the prefix is for the user and for any tool that never
learns about the field. v1 proved entries round-trip with unknown fields
preserved, but Claude Code has never been asked to *accept* a field it does not
know, so the prefix is the guarantee and the field is the convenience.

In the tree it heads a section, like any turn that starts work, but renders
distinctly — it is the join point, and the one row where the trunk visibly
gained something from elsewhere.

## 7. What this does not do

- **No merging of conversations.** Two transcripts are never interleaved. The
  only thing that crosses from a branch to the trunk is a summary you approved.
- **No automatic summarising**, no automatic rewinding, no automatic anything.
  The v1 non-goal stands: the user decides when context is summarised,
  branched, or discarded.
- **No rewriting of history.** Every operation appends a new session. Nothing
  on disk is edited or deleted, so "reset the timeline" is a presentation
  change plus a new file, never a destructive one.
- **Still not Git.** The analogy is the interaction model, not the
  implementation. There is no index, no conflict resolution, no rebase.

## 8. Failure modes

| Failure | Behaviour |
|---|---|
| No live session, so no trunk | Fall back to v1: every session a root, nothing marked |
| Summarise fails (API, auth, timeout) | Branch unchanged, error shown, nothing written |
| Summary exists but the branch's transcript is gone | Summary still appendable — it is text in the store, not a pointer |
| Append fails after the graft is written | Graft survives as an orphan session, as v1 §9 already specifies |
| Two panes summarise the same branch | Last writer wins on that one field; the store already merges per-key |
| Claude Code rejects the injected entry | The one real unknown. Mitigated by the text prefix being valid content on its own; if the `herdrTree` field is a problem it can be dropped without changing behaviour |

## 9. Changes to the v1 spec

- §13's tree model gains a summary field per branch, and the notion of a trunk
  computed at render time from `Current()`. Nothing else in the data model
  changes: graft edges are still the only stored structure.
- §25's deferral of Summarize is reversed. It was deferred on the reasoning
  that a summary is "a templated prompt, save the output" and therefore
  separable. That was wrong: in this model the summary is the mechanism by
  which exploration returns to the trunk, not documentation about it.

## 10. Open questions

1. **Does the trunk need to be visually obvious, or is position enough?** The
   trunk is at depth 0 and branches are indented, which may be sufficient
   without any marker.
2. **Should appending a summary offer to summarise first** if the chosen branch
   has none, or refuse and make the user do it explicitly? Offering is fewer
   keystrokes; refusing keeps every API call deliberate.
3. **What happens to a branch once its summary is folded back?** Nothing is
   forced. But a branch whose summary has been appended somewhere is in a
   different state from one that has not, and the tree could say so.
