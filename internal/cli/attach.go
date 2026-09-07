// Attaching the terminal to a conversation the daemon holds, rather than the
// one this process owns.
//
// It is a mode rather than a silent upgrade because the two conversations are
// separate: the daemon built its agent from config when it started, so /model
// and /connect here would change this process and not that one. Choosing
// quietly between them would leave a user unable to say which conversation
// they were talking to, and a wrong guess costs a turn against the wrong model.
package cli

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/didik-prabowo/uhai/internal/daemon"
	"github.com/didik-prabowo/uhai/internal/provider"
)

// attached says whether this front end is drawing the daemon's conversation.
// Set once at startup by Attach; nothing switches it afterwards.
var attached *daemon.Client

// Attach points this front end at the daemon, starting one if needed. It is
// an error rather than a fallback: someone who asked to attach and got a local
// conversation instead would not know which one they were in.
func Attach(ctx context.Context) error {
	socket, err := daemon.SocketHere()
	if err != nil {
		return err
	}
	c, err := daemon.Ensure(ctx, socket)
	if err != nil {
		return err
	}
	if !c.HoldsConversation(ctx) {
		return fmt.Errorf("the daemon holds no conversation — it started without a provider")
	}
	attached = c
	return nil
}

// pumpDaemon turns the daemon's events into the same messages the agent's own
// callbacks produce, so everything downstream — the screen, the spinner, the
// cost row — cannot tell which side of the socket the answer came from.
func (m *teaModel) pumpDaemon(ctx context.Context) {
	events, err := attached.Events(ctx)
	if err != nil {
		m.program.Send(teaDoneMsg{err: err})
		return
	}
	for e := range events {
		if e.Kind == daemon.EventQuestion {
			// The one event that is not a translation: it needs a reply
			// channel and a POST back, so it cannot be a pure function of the
			// event.
			m.askOnBehalfOfTheDaemon(e.Question)
			continue
		}
		if msg := daemonMsg(e); msg != nil {
			m.program.Send(msg)
		}
	}
}

// daemonMsg turns one event into the message the agent's own callback would
// have produced, so everything downstream — the screen, the spinner, the cost
// row — cannot tell which side of the socket the answer came from.
//
// A function of its own because it is the whole translation, and a terminal is
// not something these tests can drive: this is the part that can be checked.
func daemonMsg(e daemon.Event) tea.Msg {
	switch e.Kind {
	case daemon.EventDelta:
		return teaDeltaMsg(e.Text)
	case daemon.EventText:
		return teaTextMsg(e.Text)
	case daemon.EventReasoning:
		return teaThinkMsg(e.Text)
	case daemon.EventTool:
		return teaToolMsg(toolLine(e.Text, ""))
	case daemon.EventNotice:
		return teaTextMsg(teaDim.Render(e.Text))
	case daemon.EventUsage:
		if e.Usage == nil {
			return nil
		}
		return teaUsageMsg(provider.Usage{
			Input: e.Usage.Input, Output: e.Usage.Output,
			CacheRead: e.Usage.CacheRead, CacheWrite: e.Usage.CacheWrite,
		})
	}
	// done and task_done are the turn's own bookkeeping: the POST to /v1/prompt
	// returns when the turn ends, and that is what closes it here.
	return nil
}

// askOnBehalfOfTheDaemon puts the daemon's question through the same
// confirmation the local agent uses, and posts the answer back. The reply
// channel is read in a goroutine because the pump has to keep draining: a
// second question, or the answer arriving, would otherwise wait behind a
// human.
func (m *teaModel) askOnBehalfOfTheDaemon(q *daemon.Question) {
	if q == nil {
		return
	}
	// Through the same rules the local agent uses. Without this the daemon
	// asked about every call, whatever the project allowed and however many
	// times "always" had been answered — the question travelled over a socket,
	// but the decision was never anyone else's.
	if allow, decided := m.settled(q.Tool, q.Input); decided {
		go m.answerDaemon(q.ID, allow)
		return
	}
	reply := make(chan bool, 1)
	m.program.Send(teaConfirmMsg{request: teaConfirm{name: q.Tool, input: q.Input, reply: reply}})
	// The receive is inside the goroutine, not an argument to it: an argument
	// is evaluated here, which would block the pump on a human.
	go func() { m.answerDaemon(q.ID, <-reply) }()
}

// answerDaemon posts one answer back. In a goroutine because the pump has to
// keep draining: a second question, or the answer arriving, would otherwise
// wait behind a human.
func (m *teaModel) answerDaemon(id string, allowed bool) {
	if err := attached.Answer(context.Background(), id, allowed); err != nil {
		// Already answered elsewhere, or the turn was abandoned. Said rather
		// than swallowed: the user pressed a key and deserves to know it
		// decided nothing.
		m.program.Send(teaTextMsg(teaDim.Render("that question was already settled: " + err.Error())))
	}
}

// askDaemon runs one turn on the daemon and returns when it is over.
func (m *teaModel) askDaemon(ctx context.Context, prompt string) tea.Msg {
	return teaDoneMsg{err: attached.Prompt(ctx, prompt)}
}
