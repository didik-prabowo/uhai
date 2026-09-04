// Package cli is the terminal front end: tea.go is the prompt an interactive
// terminal gets, pipe.go is what a pipe gets, and commands.go holds what both
// of them do. text.go measures and cuts text for either.
package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/glamour"

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
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	return r
}
