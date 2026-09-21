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
| L | Label the selected turn (empty clears) |
| d | Cycle density: all → labelled → sessions |
| esc | Close |

## How branching works

Claude Code has no supported way to resume at a specific message, so
branching writes a new transcript containing only the ancestor chain of the
chosen turn, then resumes it. See
`docs/superpowers/specs/2026-09-21-herdr-tree-design.md`.

The transcript format is undocumented and verified against Claude Code
2.1.278. The plugin refuses to branch rather than guess when it sees a
format it has not been validated against.
