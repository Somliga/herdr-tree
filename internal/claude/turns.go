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
