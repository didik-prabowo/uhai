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
	"github.com/didik-prabowo/uhai/internal/daemon"
	"os"
	"os/signal"
	"syscall"

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/cli"
	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
	"github.com/didik-prabowo/uhai/internal/session/filestore"
)

// Run assembles an interactive session and hands it to the terminal front end.
// With resume, a saved conversation is picked up where it left off: the one
// named by id, or the newest when there is no id.
func Run(resume bool, id string) {
	a, err := newAgent()
	cli.UseStore(store)
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
	// With no id, this project's newest — not the newest anywhere. A
	// conversation about files in another tree, carried on with the agent
	// working here, acts on names that are missing or, worse, on different
	// files with the same names.
	//
	// With an id, any project: naming one is saying you know which it is, and
	// refusing then would only make the id useless.
	here, _ := os.Getwd()
	s, err := session.LatestIn(store, here)
	if id != "" {
		s, err = store.Load(id)
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
	all, err := store.All()
	if err != nil {
		return err
	}
	here, _ := os.Getwd()
	var elsewhere, unplaced int

	for _, s := range all {
		// Another project's conversation is not this project's business, and
		// the count is printed at the end so nothing simply vanishes.
		switch {
		case s.Root == "":
			unplaced++
			continue
		case !session.SameRoot(s.Root, here):
			elsewhere++
			continue
		}
		// A session saved before the pid was recorded has none, and "pid 0"
		// is a worse answer than a blank.
		held := fmt.Sprintf("pid %-7d", s.PID)
		if s.PID == 0 {
			held = fmt.Sprintf("%-11s", "")
		}
		fmt.Printf("%-13s  %s  %s %-28s %3d messages", s.ID, s.Updated.Format("2006-01-02 15:04"), held, s.Model, len(s.Messages))
		if prompt := s.Prompt(); prompt != "" {
			fmt.Printf("  %s", prompt)
		}
		fmt.Println()
	}

	// Said rather than silently dropped: 117 conversations disappearing from a
	// listing is a worse surprise than a line explaining where they went.
	if unplaced > 0 {
		fmt.Printf("\n%d older conversation(s) saved before uhai recorded the project — "+
			"resume one by id if you know it\n", unplaced)
	}
	if elsewhere > 0 {
		fmt.Printf("%d conversation(s) belong to other projects\n", elsewhere)
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
	cli.UseStore(store)

	a.OnText = func(text string) { fmt.Println(text) }
	a.OnNotice = func(text string) { fmt.Fprintln(os.Stderr, text) }
	a.OnToolCall = func(name, input string) { fmt.Fprintf(os.Stderr, "⎿ %s\n", name) }
	a.Confirm = func(string, string) bool { return allowTools }
	// Without this a -p turn is paid for and recorded nowhere, so -resume
	// picks the conversation up believing it has cost nothing so far.
	a.OnUsage = func(u provider.Usage) { cli.RecordUsage(a, u) }

	// Without a terminal in raw mode, Ctrl+C arrives as a signal again.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err = a.Ask(ctx, prompt)
	// Saved whether or not the turn worked, the way both other front ends do
	// it: a turn that died halfway has already been paid for, and the history
	// closeTurn left behind is what a -resume would carry on from. The Ask
	// error is what the caller gets — a script's exit code is about the
	// answer, not about the bookkeeping.
	if serr := cli.SaveSession(a); serr != nil {
		fmt.Fprintln(os.Stderr, "uhai: the session is not being saved:", serr)
	}
	return err
}

// store is where conversations are kept. One line, in the composition root,
// because that is the whole point of the Store contract: the choice is made
// once here and nothing downstream repeats it. A second backend replaces this
// expression and changes nothing else.
var store session.Store = filestore.New("")

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

// RunDaemon runs the agent as a process the terminal can outlive, until it is
// interrupted. It is in the foreground on purpose for now: a daemon that
// forks itself before it is trusted is a daemon nobody can watch, and the one
// thing worth knowing about this one is what it does while it runs.
//
// It owns nothing yet but the socket. Moving the agent behind it is the next
// step and a larger one — this is the transport, proved on its own.
func RunDaemon() error {
	socket, err := daemon.SocketHere()
	if err != nil {
		return err
	}
	if _, err := config.Dir(); err != nil {
		return err
	}
	if dir, derr := config.Dir(); derr == nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	// The daemon builds its own agent from the same config the front end
	// reads, and hands the sub-agent the read-only rules /bg already used:
	// nobody is watching to answer a confirmation, so anything that writes or
	// runs commands is refused and the model is told why. That is exactly why
	// background work is the first thing worth moving here — it never needed
	// the permission round trip that the main conversation does.
	run := func(ctx context.Context, prompt string) (string, int, error) {
		a, err := newAgent()
		if err != nil {
			return "", 0, err
		}
		a.Confirm = func(string, string) bool { return false }

		var report string
		a.OnText = func(text string) { report = text }
		a.OnNotice = func(string) {}
		a.OnToolCall = func(string, string) {}

		err = a.Ask(ctx, prompt)
		return report, a.Tokens(), err
	}
	if _, perr := config.LoadProvider(); perr != nil {
		run = nil // it can still serve sessions and events, and says so
		fmt.Fprintln(os.Stderr, "uhai: no provider connected, so the daemon cannot run tasks:", perr)
	}

	// The conversation the daemon holds. One agent, built once, its callbacks
	// turned into events on the socket — which is the same job the TUI's
	// callbacks do when the agent is in its own process.
	var srv *daemon.Server
	var conversation *agent.Agent

	prompt := func(ctx context.Context, text string) (string, int, error) {
		if conversation == nil {
			a, err := newAgent()
			if err != nil {
				return "", 0, err
			}
			a.OnText = func(t string) { srv.Publish(daemon.Event{Kind: daemon.EventText, Text: t}) }
			a.OnDelta = func(d string) { srv.Publish(daemon.Event{Kind: daemon.EventDelta, Text: d}) }
			a.OnReasoning = func(d string) { srv.Publish(daemon.Event{Kind: daemon.EventReasoning, Text: d}) }
			a.OnNotice = func(t string) { srv.Publish(daemon.Event{Kind: daemon.EventNotice, Text: t}) }
			a.OnToolCall = func(name, input string) {
				srv.Publish(daemon.Event{Kind: daemon.EventTool, Text: name})
			}
			a.OnUsage = func(u provider.Usage) {
				srv.Publish(daemon.Event{Kind: daemon.EventUsage, Usage: &daemon.Usage{
					Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
				}})
			}
			// The question round trip, which is the whole reason this could
			// not have been done before it existed.
			a.Confirm = func(name, input string) bool { return srv.Ask(ctx, name, input) }
			conversation = a
		}
		err := conversation.Ask(ctx, text)
		// Saved every turn, the way the front ends do it: a daemon that is
		// killed should lose no more than a terminal that is closed.
		if serr := cli.SaveSession(conversation); serr != nil {
			fmt.Fprintln(os.Stderr, "uhai: the session is not being saved:", serr)
		}
		return "", conversation.Tokens(), err
	}
	if _, perr := config.LoadProvider(); perr != nil {
		prompt = nil
	}

	srv = daemon.NewServer(store, run, prompt)
	if err := srv.Listen(socket); err != nil {
		return fmt.Errorf("could not listen on %s: %w", socket, err)
	}
	here, _ := os.Getwd()
	fmt.Fprintln(os.Stderr, "uhai: daemon for", here, "listening on", socket)

	// The socket is a file. A daemon killed without clearing it leaves the
	// next one to do it, which works, but only because removeStale exists —
	// tidying up after ourselves is cheaper than relying on that.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		fmt.Fprintln(os.Stderr, "uhai: daemon stopping")
		srv.Close()
		os.Remove(socket)
	}()

	return srv.Serve()
}

// RunAttached opens the terminal on the conversation the daemon holds, rather
// than starting one here. A daemon is started if there is none.
//
// A mode rather than a default, because the two conversations are separate:
// the daemon built its agent from config when it started, so /model and
// /connect in an attached terminal would change this process and not the one
// answering. Making the choice explicit is the difference between a user who
// knows which conversation they are in and one who cannot tell.
func RunAttached() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := cli.Attach(ctx); err != nil {
		return err
	}
	// The local agent is still built: it holds the tools list, the model name
	// for the status row, and everything a slash command reads. It just never
	// answers a prompt.
	a, err := newAgent()
	cli.UseStore(store)
	cli.Run(a, err)
	return nil
}
