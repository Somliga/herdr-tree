package claude

import (
	"os"
	"strings"

	"herdr-tree/internal/adapter"
)

// syntheticResume is written by Claude Code as a user entry whenever a
// session is resumed. It is not something the user typed.
const syntheticResume = "Continue from where you left off."

func shortenHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return "~" + p[len(home):]
}

// IsPrompt reports whether an entry is something a human typed. Kept for
// Select's asymmetry: the tree hides injected entries, the graft keeps them.
func IsPrompt(e Entry) bool {
	k, keep := Classify(e, HasHumanOrigin([]Entry{e}))
	return keep && k == adapter.KindHuman
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
