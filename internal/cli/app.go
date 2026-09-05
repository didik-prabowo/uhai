// Package cli is the terminal front end: tea.go is the prompt an interactive
// terminal gets, pipe.go is what a pipe gets, and commands.go holds what both
// of them do. text.go measures and cuts text for either.
package cli

import (
	"fmt"
	"os"

	"reflect"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/didik-prabowo/uhai/internal/agent"
)

// Run takes over the terminal and blocks until the user quits or stdin ends.
// startupErr, if any, is shown once the screen is ready — a session with no
// provider still opens, so the user can /connect from inside.
func Run(a *agent.Agent, startupErr error) {
	restore, raw := rawMode()
	restore() // bubbletea puts the terminal into raw mode itself; this only probed
	if !raw {
		runPipe(a, startupErr)
		return
	}
	if err := runTea(a, startupErr); err != nil {
		fmt.Fprintln(os.Stderr, "uhai:", err)
	}
	// The id is printed on the way out because that is the moment it is
	// needed and the last moment it is free: hunting for it later means
	// -sessions and reading timestamps.
	if report := spentReport(a.Provider); report != "" {
		fmt.Printf("\n%s\n", report)
	}
	if id := savedSessionID(); id != "" {
		fmt.Printf("\nResume this conversation with:\n  uhai -resume %s\n", id)
	}
}

// rawMode reports whether we are talking to a terminal at all. A pipe is not
// one, and gets the loop in pipe.go instead.
func rawMode() (restore func(), ok bool) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return func() {}, false
	}
	return func() {}, info.Mode()&os.ModeCharDevice != 0
}

// newRenderer renders the model's markdown. A nil renderer is not fatal — the
// text is then printed as it came.
func newRenderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(codeBlockStyle()),
		glamour.WithWordWrap(width),
		// Glamour detects the terminal's colours for itself, and lands on a
		// different answer from lipgloss often enough to matter: the band
		// drawn around a code block and the one chroma paints behind the
		// characters have to be the same shade, and 256-colour next to
		// truecolour is two shades.
		glamour.WithColorProfile(lipgloss.ColorProfile()),
	)
	return r
}

// codeBlockStyle is the dark style with a background on every chroma token.
//
// The colour itself does not matter and never reaches the screen — rendererText
// strips it and paints its own band, which is the only way to get one shade
// across the whole block. What matters is that it is *there*: a background is
// the mark that says which rendered lines were code, and after glamour has run
// there is nothing else left to tell them apart by.
//
// It has to go on the tokens rather than on the code block. Chroma draws the
// characters and a background set on the block around them is ignored, and
// glamour's IndentToken, which would have drawn a gutter bar instead, is not
// honoured for code blocks — both were tried first.
//
// Reflection rather than thirty-five assignments, and with the better failure
// mode: a token type glamour adds later is covered without anyone noticing it
// was missing.
func codeBlockStyle() ansi.StyleConfig {
	style := styles.DarkStyleConfig
	if style.CodeBlock.Chroma == nil {
		return style
	}
	background := codeBackground()

	chroma := reflect.ValueOf(style.CodeBlock.Chroma).Elem()
	for i := 0; i < chroma.NumField(); i++ {
		if field := chroma.Field(i); field.Type() == reflect.TypeOf(ansi.StylePrimitive{}) {
			field.FieldByName("BackgroundColor").Set(reflect.ValueOf(&background))
		}
	}
	return style
}
