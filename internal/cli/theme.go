// Every colour the terminal front end uses, in one place. Scattered literals
// are how a palette drifts: one file gets a new accent, three keep the old one,
// and nobody can say what the scheme is any more.
//
// Slate for everything structural, one violet accent for what the eye should
// land on, and softened red and green for a diff — saturated ones fight the
// text around them. Adaptive where it matters, so a light terminal does not get
// dark-theme colours painted onto it.
package cli

import "github.com/charmbracelet/lipgloss"

var (
	accent = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"} // violet
	text   = lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#E2E8F0"}
	muted  = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	line   = lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#334155"} // rules and borders
	band   = lipgloss.AdaptiveColor{Light: "#EEF2FF", Dark: "#1E293B"} // a quiet background
	onAcc  = lipgloss.AdaptiveColor{Light: "#F8FAFC", Dark: "#0F172A"} // text on the accent

	// codeBG is the band behind a fenced code block: a neutral grey, lifted
	// off the page rather than sunk into it. Neutral on purpose — every other
	// colour here is slate, which carries a blue cast, and a code block tinted
	// the same as the furniture never quite looks like a separate object.
	// Deliberately not band, which is the question's and is tinted with the
	// accent: the two sit within a screen of each other.
	codeBG     = lipgloss.AdaptiveColor{Light: "#ECECEC", Dark: "#2B2B2B"}
	codeNumber = lipgloss.AdaptiveColor{Light: "#6B6B6B", Dark: "#808080"}

	added   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#86EFAC"}
	removed = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"}
)

var (
	teaTitle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	teaUser  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	teaDim   = lipgloss.NewStyle().Foreground(muted)

	// teaAsk is how a question of the user's own reads back in the chat: a
	// full-width band, so it stands apart from the answer under it.
	teaAsk = lipgloss.NewStyle().Foreground(text).Background(band).Padding(0, 1)

	// Top and bottom rules only, and no side borders at all: an enabled side
	// with an empty character still costs a column, which left the whole form
	// standing one short of the edge.
	teaForm = lipgloss.NewStyle().
		Border(lipgloss.Border{Top: "─", Bottom: "─"}).
		BorderLeft(false).
		BorderRight(false).
		BorderForeground(line).
		Padding(0)
)

var (
	teaAdded   = lipgloss.NewStyle().Foreground(added)
	teaRemoved = lipgloss.NewStyle().Foreground(removed)

	// The question wears a box of its own, so it reads as something waiting
	// for an answer rather than as more chat.
	confirmBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Padding(0, 1)

	confirmHeading  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	confirmQuestion = lipgloss.NewStyle().Foreground(text)
	confirmChoice   = lipgloss.NewStyle().Foreground(muted)

	// The answer under the cursor is a band rather than a colour alone: at a
	// glance, the shape says where you are.
	confirmChosen = lipgloss.NewStyle().Bold(true).Foreground(onAcc).Background(accent)
)
