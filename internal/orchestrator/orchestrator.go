// Package orchestrator is ouhai's composition root: the one place that says
// which parts make up a running ouhai and in what order they are built.
//
// It exists so the pieces stay independent of each other — the agent does not
// know where its provider came from, the terminal does not know how the agent
// was configured. The interactive session and the one-shot run below share
// every part except the front end.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/didik-prabowo/ouhai/internal/agent"
	"github.com/didik-prabowo/ouhai/internal/cli"
	"github.com/didik-prabowo/ouhai/internal/config"
)

// Run assembles an interactive session and hands it to the terminal front end.
// With resume, the newest saved conversation is picked up where it left off.
func Run(resume bool) {
	a, err := newAgent()
	if resume {
		if err := restore(a); err != nil {
			fmt.Fprintln(os.Stderr, "ouhai:", err)
		}
	}
	cli.Run(a, err)
}

// restore puts the newest saved conversation back into the agent. Failing to
// find one is worth saying out loud — silently starting empty would look like
// the history was lost.
func restore(a *agent.Agent) error {
	s, err := config.LatestSession()
	if err != nil {
		return err
	}
	a.History = s.Messages
	return nil
}

// RunOnce answers one prompt and exits, for scripts and pipes. Text goes to
// stdout so it can be piped; progress goes to stderr so it does not pollute
// that. Tools that write files or run commands are refused unless allowTools
// is set, because there is nobody here to ask.
func RunOnce(prompt string, allowTools bool) error {
	a, err := newAgent()
	if err != nil {
		return err // no provider is fatal here: nothing can be done about it
	}

	a.OnText = func(text string) { fmt.Println(text) }
	a.OnNotice = func(text string) { fmt.Fprintln(os.Stderr, text) }
	a.OnToolCall = func(name, input string) { fmt.Fprintf(os.Stderr, "⎿ %s\n", name) }
	a.Confirm = func(string, string) bool { return allowTools }

	// Without a terminal in raw mode, Ctrl+C arrives as a signal again.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return a.Ask(ctx, prompt)
}

// newAgent builds the agent from the saved settings, the credentials, and the
// project's own instructions. A provider that cannot be built is not fatal for
// every caller: the interactive session opens without one so the user can
// /connect from inside, and the reason is passed on to be shown.
func newAgent() (*agent.Agent, error) {
	p, err := config.LoadProvider()
	a := agent.New(p) // p may be nil; checked before Ask
	if p != nil {
		a.MaxContextTokens = config.ContextWindow(p.Name())
		a.UseTools = config.SupportsTools(p.Name())
	}
	a.AllowTool = func(name string) bool { return !config.ToolDenied(name) }
	if notes := config.ProjectNotes(); notes != "" {
		a.System += "\n\n# Project instructions\nThese come from OUHAI.md, AGENTS.md or CLAUDE.md in the working directory. Follow them.\n\n" + notes
	}
	// Only the names travel with every prompt; the instructions themselves are
	// a file to open when the work turns out to be that work.
	if skills := config.SkillNotes(); skills != "" {
		a.System += "\n\n# Skills\nThis project keeps instructions for particular jobs. When one of these covers what you are asked to do, read its file before starting.\n\n" + skills
	}
	return a, err
}
