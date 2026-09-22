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

## 4. Summarising a range

On demand, never automatically, and over a **range of turns** rather than a
whole session.

Where a summary is needed and none exists, the tree **offers** to make one; it
never makes one unprompted and never refuses to proceed. Choosing to fold a
branch back that has not been summarised leads to the summarise step, with its
cost shown, and the user can decline and do something else. Forcing the user to
summarise first as a separate ritual would be pedantry; summarising silently
would spend their money without asking.

`s` on a row fixes the END of the range — the work you have just finished is
almost always what you want summarised, and it is where you already are. The
cursor then moves to choose the START, with the range highlighted as you move.
Confirm, and the summary is generated and stored against that range.

A range rather than a session because the two uses want different spans:

- **On a branch**, the range is usually the whole branch: fold an exploration
  back to where it started.
- **On the trunk**, the range is a segment, and summarising it is
  **compaction**. Summarise turns 5–12, rewind to 5, seed with the summary, and
  eight turns of exploration become one paragraph you carry on from. The
  original turns are still on disk, still reachable in the tree, and still
  branchable.

Compaction is the same operation as fold-back with the range's own start as the
destination. That is now the third feature that turns out to be rewind-plus-seed,
which is the clearest argument that the model has the right primitive.

### How the range is summarised

`Graft` the source session at the range's END into a temporary session, resume
it with a prompt naming the START turn, capture stdout, delete the temporary
session. The graft is needed because `--resume` always continues at a session's
tip, and a summary of "up to turn 12" must not see turns 13 onward.

The model therefore sees everything from the session's beginning up to the end
of the range, and is asked to summarise only the segment. That is deliberate:
the earlier context is what lets it summarise the segment accurately, and it
costs tokens for a prefix it will not describe. On a long trunk that is the
expensive part of this feature, and the confirmation should say so — the
existing confirm dialog already reports bytes, and this one should too.

The prompt is a template, configurable later. It should ask for: what was being
attempted, what was decided, what was rejected and why, and what was left
unfinished. The "why not" matters most — it is the part that stops the trunk
re-exploring a dead end it has already paid for.

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

### Choosing where it lands

Folding a summary back asks two things: which summary, and where it goes. The
destination defaults to **the end of the parent** — carry on from where you
are, now knowing what the branch found — and that is the common case. You can
also pick any earlier turn in the parent, which rewinds it as well as seeding
it.

Those two destinations want different mechanisms, and using the natural one for
each matters:

**At the parent's end, when the parent is the live session.** The summary is
simply your next message. Herdr can deliver it: `herdr agent prompt <agent>
"<summary>"` sends text to the running agent as if typed. No graft, no copy, no
new session — the summary becomes the next turn of the conversation you are
already in. Given a transcript here is already 6.6 MB, copying one per fold-back
to achieve what a single message achieves would be indefensible.

**Anywhere else** — an earlier turn, or a parent that is not live — is
rewind-plus-seed as described in §5. A graft is genuinely needed there, because
the timeline is changing shape.

The first case has a precondition: the agent must be able to accept input.
Herdr reports `agent_blocked` when an agent is sitting at an approval or
question dialog, and refuses to send. The tree must surface that rather than
appear to succeed — and must not fall back to grafting silently, because the
user asked to continue a conversation, not to fork one.

## 5b. Cascading fold-back

Branches nest, and so do summaries. Branch from the trunk, branch again from
that branch, summarise the inner one and append it to the outer, then summarise
the outer — which now contains the inner summary as ordinary conversation — and
append that to the trunk.

Nothing special is needed for this. Each fold-back is a rewind-plus-seed on the
immediate parent, and a summary that absorbed another summary is just text. The
only property the design must preserve is that a summary produced AFTER a
fold-back includes the folded-in material, which it does automatically because
the injected entry is part of the transcript being summarised.

What the tree owes the user here is legibility: a point that absorbed a branch
should say so, and it should still say so after the containing branch is itself
folded somewhere else.

## 6. How an injected summary appears

It is not something you typed, and the tree must not pretend otherwise.

The injected entry is marked two ways: a visible prefix in its text, and a
`herdrTree` object on the entry recording `{kind: "summary", from: <branch id>}`.
The field is for us, the prefix is for the user and for any tool that never
learns about the field. v1 proved entries round-trip with unknown fields
preserved, but Claude Code has never been asked to *accept* a field it does not
know, so the prefix is the guarantee and the field is the convenience.

In the tree it heads a section, like any turn that starts work, but renders
distinctly. Proposed: a `⤶` marker and the source branch's short id, so the row
reads as a join rather than as something you typed:

```
  user: thin slice is fine, go with that                    (27)
  ⤶ summary of f2af34a4 — redis-backed sessions             (14)
  user: right, carry on with the token service               (9)
```

This is the one row where the trunk visibly gained something from elsewhere,
and it is the thing that makes a rewound timeline readable a week later. It
must survive the containing branch being folded onward — the marker belongs to
the entry, not to a store record that a later rewind might not carry.

## 6b. Colour

Colour carries two things: which kind of row this is, and — for a summary —
what it did to the timeline.

| row | colour | glyph |
|---|---|---|
| your prompts | foreground, bright | — |
| assistant replies | dimmed | — |
| tool calls | dimmest | `[name: arg]` |
| summary, **import** | orange | `⤶` |
| summary, **compaction** | blue | `⤶` |
| unreadable session | red | `⚠` |
| current session's tip | green | `●` |

**The summary colours encode effect, not origin.** Blue means these turns were
on this line and got replaced by something shorter: the line contracted,
nothing new arrived. Orange means knowledge came in from a line that was
abandoned: something is here that was not before.

Origin — "from the trunk" versus "from a branch" — is the obvious encoding and
the wrong one, because it stops being crisp the moment fold-backs cascade. A
summary folded in from a branch that itself absorbed another branch's summary
is still, unambiguously, an import. "Did new knowledge arrive here?" survives
nesting; "where was it before?" does not.

Blue and orange are also the colour-vision-safe axis. The common deficiencies
affect red and green; blue against orange stays distinguishable.

### Colour reinforces, it never carries alone

Every distinction above is also a glyph or a prefix. Colour is lost on copy and
paste, in a log, in a screenshot someone re-encodes, and against a terminal
theme that fights it — and this plugin runs inside Herdr, which has themes and
a light/dark auto-switch. A user who cannot see the colour must lose nothing.

Colours are therefore `lipgloss.AdaptiveColor`, resolved per light or dark
background rather than fixed ANSI, and the palette is one table in one place so
it can be made configurable without touching the renderer.

### `renderRow` stays plain text

`renderRow` returns an uncoloured string and a style key; `View` applies the
style. This is not fastidiousness: the row content is asserted by a large
number of tests, several of which caught real defects during v1 precisely
because they could compare exact output. Styling inside `renderRow` would make
every one of those assertions fight escape sequences.

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

**Settled during review:**

- The trunk needs no marker. Depth 0 against indented branches is enough.
- Summarising is always offered, never forced and never silent.
- Fold-back destination defaults to the end of the parent, with any earlier
  turn selectable.

**Still open:**

1. **Range selection direction.** `s` fixes the end and the cursor picks the
   start, which means moving backwards through the list. That matches how the
   thought arrives — "summarise what I just did" — but it is the opposite of
   how most range selections work. Worth trying before committing.
2. **What happens to a branch once its summary is folded back?** Nothing is
   forced. But a branch whose summary has been appended somewhere is in a
   different state from one that has not, and the tree could say so.
3. **Does `herdr agent prompt` deliver reliably enough to depend on?** It
   honours bracketed paste and reports submission, but the Herdr documentation
   is explicit that successful submission does not prove the agent started a
   turn. A multi-paragraph summary is a large paste. This needs testing against
   a real agent before the tip-append path is trusted.
