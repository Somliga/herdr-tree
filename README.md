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
| ⏎ | Continue from the selected turn — resumes in place if it is the latest, branches otherwise |
| s | Summarise: fixes the END of a range, then move to its start and press `s` or ⏎ |
| p | Fold a summary back in at the selected turn |
| L | Label the selected turn (empty clears) |
| a | Toggle scope: this session ↔ all sessions |
| f | Cycle filter: all entries ↔ only what a person typed |
| esc | Cancel the range being selected, or close the overlay |

While a range is being selected the footer says so, and `esc` cancels the
range rather than closing.

## Summarising and folding back

`s` summarises a range of turns, on demand and never automatically. It costs a
real model call: the summary is produced by reading the session up to the end
of the range, so the confirmation reports what that costs before anything is
spent. The summary is stored in the tree; the session is not touched.

`p` appends a stored summary at the selected turn. Two things can happen, and
the picker says which before you commit:

- **At the tip of the session you are in**, the summary is simply your next
  message and Herdr delivers it to the running agent. Nothing is copied.
- **Anywhere else**, it starts a new session that rewinds to that turn and
  carries the summary as its first turn.

The first case needs to know where the live session is running. Herdr accepts
a pane id wherever it accepts an agent, and `herdr agent list` reports each
live agent's session id, so the session resolves to a pane. If nothing is
holding the session — it was closed, or opened somewhere Herdr cannot see —
the second path is taken instead, which is right: there is no conversation to
continue. The picker states which one it is about to do, so the difference is
never silent.

A summary of a *different* session arriving here is an **import** — knowledge
came in from a line that was abandoned. A summary of *this* session's own turns
is a **compaction** — the line contracted and nothing new arrived. Both are the
same operation; the injected turn's `⤶` prefix is what tells them apart, in the
tree and to any tool that reads the transcript later.

## How branching works

Claude Code has no supported way to resume at a specific message, so
branching writes a new transcript containing only the ancestor chain of the
chosen turn, then resumes it. See
`docs/superpowers/specs/2026-09-21-herdr-tree-design.md`.

The transcript format is undocumented and verified against Claude Code
2.1.278. The plugin refuses to branch rather than guess when it sees a
format it has not been validated against.
