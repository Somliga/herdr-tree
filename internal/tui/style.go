package tui

import (
	"github.com/charmbracelet/lipgloss"

	"herdr-tree/internal/adapter"
	"herdr-tree/internal/tree"
)

type StyleKey int

const (
	StyleHuman StyleKey = iota
	StyleAssistant
	StyleTool
	StyleImport
	StyleCompaction
	StyleBroken
	StyleCurrent
)

// The palette lives here alone so it can be made configurable without
// touching the renderer. AdaptiveColor resolves per light or dark background;
// Herdr has themes and a light/dark auto-switch, so fixed ANSI would fight it.
//
// Import is orange and compaction blue: the colour-vision-safe axis, since the
// common deficiencies affect red and green. The two encode EFFECT — did new
// knowledge arrive, or did this line merely contract — because origin stops
// being crisp once fold-backs cascade.
var palette = map[StyleKey]lipgloss.Style{
	StyleHuman:      lipgloss.NewStyle().Bold(true),
	StyleAssistant:  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "244", Dark: "247"}),
	StyleTool:       lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "248", Dark: "240"}),
	StyleImport:     lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "166", Dark: "215"}),
	StyleCompaction: lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "26", Dark: "75"}),
	StyleBroken:     lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"}),
	StyleCurrent:    lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "114"}),
}

// styleFor picks the row's style key from what produced it, not from where it
// sits. A broken session always wins: an unreadable transcript matters more
// than what kind of entry it claims to be. The tip you are sitting on wins
// next — "you are here" outranks "this was a prompt".
//
// There is deliberately no trunk style. Spec §6b's table has no trunk row, and
// the trunk is already carried by depth: an earlier draft of this file gave
// the trunk an EMPTY style, which de-emphasised the main line relative to the
// branches it outranks.
func styleFor(n *tree.Node, currentTip bool) StyleKey {
	if n.Broken {
		return StyleBroken
	}
	if currentTip {
		return StyleCurrent
	}
	switch n.Node.Kind {
	case adapter.KindAssistant:
		return StyleAssistant
	case adapter.KindToolCall:
		return StyleTool
	case adapter.KindSummaryImport:
		return StyleImport
	case adapter.KindSummaryCompaction:
		return StyleCompaction
	}
	return StyleHuman
}

// render applies a style to text. renderRow itself stays plain text so its
// output remains assertable; View is the only caller of render.
func render(s StyleKey, text string) string {
	st, ok := palette[s]
	if !ok {
		return text
	}
	return st.Render(text)
}
