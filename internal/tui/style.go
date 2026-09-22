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
	StyleTrunk
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
	StyleTrunk:      lipgloss.NewStyle(),
}

// styleFor picks the row's style key from what produced it, not from where it
// sits. A broken session always wins: an unreadable transcript matters more
// than what kind of entry it claims to be.
func styleFor(n *tree.Node, onTrunk bool) StyleKey {
	if n.Broken {
		return StyleBroken
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
	if onTrunk {
		return StyleTrunk
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
