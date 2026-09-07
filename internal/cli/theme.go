// Every colour the terminal front end uses, in one place. Scattered literals
// are how a palette drifts: one file gets a new accent, three keep the old one,
// and nobody can say what the scheme is any more.
//
// Slate for everything structural, one violet accent for what the eye should
// land on, and softened red and green for a diff — saturated ones fight the
// text around them. Each is a light/dark pair, so a light terminal does not get
// dark-theme colours painted onto it.
//
// The pair is resolved once, by useTheme, rather than at every use. lipgloss v2
// dropped AdaptiveColor for LightDark(isDark), which asks the caller to know
// which it is — and the terminal only says so once the program has started. So
// the palette and the styles built from it are assigned together, and every
// view goes on naming `accent` and `teaDim` exactly as it did.
package cli

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// A light and a dark spelling of one colour.
type pair struct{ light, dark color.Color }

func hex(light, dark string) pair {
	return pair{lipgloss.Color(light), lipgloss.Color(dark)}
}

var (
	accentPair = hex("#6D28D9", "#A78BFA") // violet
	textPair   = hex("#0F172A", "#E2E8F0")
	mutedPair  = hex("#64748B", "#94A3B8")
	linePair   = hex("#CBD5E1", "#334155") // rules and borders
	bandPair   = hex("#EEF2FF", "#1E293B") // a quiet background
	onAccPair  = hex("#F8FAFC", "#0F172A") // text on the accent

	// codeBG is the band behind a fenced code block: a neutral grey, lifted
	// off the page rather than sunk into it. Neutral on purpose — every other
	// colour here is slate, which carries a blue cast, and a code block tinted
	// the same as the furniture never quite looks like a separate object.
	// Deliberately not band, which is the question's and is tinted with the
	// accent: the two sit within a screen of each other.
	codeBGPair     = hex("#ECECEC", "#2B2B2B")
	codeNumberPair = hex("#6B6B6B", "#808080")

	addedPair   = hex("#15803D", "#86EFAC")
	removedPair = hex("#B91C1C", "#FCA5A5")
)

// The palette, resolved. Dark until the terminal says otherwise, which is what
// it almost always is and what a test gets without asking.
var (
	accent, text, muted, line, band, onAcc color.Color
	codeBG, codeNumber                     color.Color
	added, removed                         color.Color
)

// The styles built from it.
var (
	teaTitle, teaUser, teaDim lipgloss.Style

	// teaAsk is how a question of the user's own reads back in the chat: a
	// full-width band, so it stands apart from the answer under it.
	teaAsk lipgloss.Style

	// Top and bottom rules only, and no side borders at all: an enabled side
	// with an empty character still costs a column, which left the whole form
	// standing one short of the edge.
	teaForm lipgloss.Style

	teaAdded, teaRemoved lipgloss.Style

	// The question wears a box of its own, so it reads as something waiting
	// for an answer rather than as more chat.
	confirmBox lipgloss.Style

	confirmHeading, confirmQuestion, confirmChoice lipgloss.Style

	// The answer under the cursor is a band rather than a colour alone: at a
	// glance, the shape says where you are.
	confirmChosen lipgloss.Style
)

func init() { useTheme(true) }

// useTheme resolves the palette for a light or a dark terminal, and rebuilds
// every style that reads from it. Called at startup, once the terminal has
// answered, and again by a test that wants the other half of each pair.
func useTheme(isDark bool) {
	pick := lipgloss.LightDark(isDark)
	of := func(p pair) color.Color { return pick(p.light, p.dark) }

	accent, text, muted = of(accentPair), of(textPair), of(mutedPair)
	line, band, onAcc = of(linePair), of(bandPair), of(onAccPair)
	codeBG, codeNumber = of(codeBGPair), of(codeNumberPair)
	added, removed = of(addedPair), of(removedPair)

	teaTitle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	teaUser = lipgloss.NewStyle().Bold(true).Foreground(accent)
	teaDim = lipgloss.NewStyle().Foreground(muted)
	teaAsk = lipgloss.NewStyle().Foreground(text).Background(band).Padding(0, len(chatGutter))
	teaForm = lipgloss.NewStyle().
		Border(lipgloss.Border{Top: "─", Bottom: "─"}).
		BorderLeft(false).
		BorderRight(false).
		BorderForeground(line).
		Padding(0)

	teaAdded = lipgloss.NewStyle().Foreground(added)
	teaRemoved = lipgloss.NewStyle().Foreground(removed)

	confirmBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1)

	confirmHeading = lipgloss.NewStyle().Bold(true).Foreground(accent)
	confirmQuestion = lipgloss.NewStyle().Foreground(text)
	confirmChoice = lipgloss.NewStyle().Foreground(muted)
	confirmChosen = lipgloss.NewStyle().Bold(true).Foreground(onAcc).Background(accent)
}
