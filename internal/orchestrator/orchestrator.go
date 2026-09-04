// Package orchestrator is uhai's composition root: the one place that says
// which parts make up a running uhai and in what order they are built.
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

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/cli"
	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
)

// Run assembles an interactive session and hands it to the terminal front end.
// With resume, a saved conversation is picked up where it left off: the one
// named by id, or the newest when there is no id.
func Run(resume bool, id string) {
	a, err := newAgent()
	if resume {
		if note, rerr := restore(a, id); rerr != nil {
			fmt.Fprintln(os.Stderr, "uhai:", rerr)
		} else if note != "" {
			fmt.Fprintln(os.Stderr, "uhai:", note)
		}
	}
	cli.Run(a, err)
}

// restore puts a saved conversation back into the agent, and points it at the
// model that conversation was held with: resuming on whatever the settings say
// today would change the context window and the price of every turn without
// saying so. The note is what could not be restored, which is worth a line —
// silently carrying on with a different model is the failure worth avoiding.
//
// Failing to find the session at all is an error: starting empty would look
// like the history was lost.
func restore(a *agent.Agent, id string) (string, error) {
	s, err := config.LatestSession()
	if id != "" {
		s, err = config.LoadSession(id)
	}
	if err != nil {
		return "", err
	}

	a.History = s.Messages
	// Saving continues in the file that was resumed, so an id survives being
	// picked up and put down rather than forking a copy each time.
	cli.ContinueSession(s)

	if s.Model == "" || (a.Provider != nil && a.Provider.Name() == s.Model) {
		return "", nil
	}
	p, perr := config.LoadProviderFor(s.Model)
	if perr != nil {
		return fmt.Sprintf("this conversation was held with %s, carrying on with what settings.json says: %v", s.Model, perr), nil
	}
	use(a, p)
	return "", nil
}

// use points the agent at a provider, along with the two facts that follow the
// model rather than the session.
func use(a *agent.Agent, p provider.Provider) {
	a.Provider = p
	a.MaxContextTokens = config.ContextWindow(p.Name())
	a.UseTools = config.SupportsTools(p.Name())
}

// ListSessions prints what can be resumed, newest first. It is a command
// rather than a screen: the answer is usually one id, and copying it out of a
// terminal beats arrowing through a list.
func ListSessions() error {
	all, err := config.Sessions()
	if err != nil {
		return err
	}
	for _, s := range all {
		fmt.Printf("%s  %s  %-28s %3d messages", s.ID, s.Updated.Format("2006-01-02 15:04"), s.Model, len(s.Messages))
		if prompt := s.Prompt(); prompt != "" {
			fmt.Printf("  %s", prompt)
		}
		fmt.Println()
	}
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
		use(a, p)
	}
	a.AllowTool = func(name string) bool { return !config.ToolDenied(name) }
	if notes := config.ProjectNotes(); notes != "" {
		a.System += "\n\n# Project instructions\nThese come from UHAI.md, AGENTS.md or CLAUDE.md in the working directory. Follow them.\n\n" + notes
	}
	// Only the names travel with every prompt; the instructions themselves are
	// a file to open when the work turns out to be that work.
	if skills := config.SkillNotes(); skills != "" {
		a.System += "\n\n# Skills\nThis project keeps instructions for particular jobs. When one of these covers what you are asked to do, read its file before starting.\n\n" + skills
	}
	return a, err
}
