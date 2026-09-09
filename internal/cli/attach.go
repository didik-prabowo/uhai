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
	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/daemon"
	"github.com/didik-prabowo/uhai/internal/provider"
)

// attached says whether this front end is drawing the daemon's conversation.
// Set once at startup by Attach; nothing switches it afterwards.
var attached *daemon.Client

// attachedModel is the model the daemon answers with, read once at attach.
// The local agent's provider is not it — /model here would change this process
// and not that one — so everything that names a model reads this instead.
var attachedModel string

// attachedHolding is the conversation the daemon was already carrying when
// this terminal arrived. Read once at attach, and for the same reason as the
// model: the local agent is not the one that remembers anything.
var attachedHolding daemon.Holding

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
	holding, holds := c.Conversation(ctx)
	if !holds {
		return fmt.Errorf("the daemon holds no conversation — it started without a provider")
	}
	attached, attachedModel, attachedHolding = c, holding.Model, holding
	return nil
}

// resumedHistory is the conversation being drawn as it stood before this
// terminal opened: the daemon's when attached, this process's otherwise.
//
// The opening line read the local agent either way, and attached that history
// is empty — so a daemon that had picked the morning back up off disk was met
// with a blank screen saying nothing, and the first prompt was answered with a
// context the person typing it could not see.
func resumedHistory(a *agent.Agent) (msgs, tokens int) {
	if attached != nil {
		return attachedHolding.Resumed, attachedHolding.ResumedTokens
	}
	return len(a.History), a.Tokens()
}

// answeringModel is the model a turn would run against: the daemon's when
// attached, this process's provider otherwise. They are different things and
// the row used to show the second while the first did the answering.
func (m *teaModel) answeringModel() string {
	if attached != nil {
		if attachedModel == "" {
			return "the daemon's"
		}
		return attachedModel
	}
	return providerLabel(m.agent.Provider)
}

// pricingModel is the model a turn is billed against: the daemon's when
// attached, this process's otherwise. Empty when there is nothing to price,
// which is what config.CostUSD already answers "" to.
//
// The status row was taught to name the daemon's model and left pricing the
// local one, which is the same bug one column to the right. Both halves are
// wrong and in opposite directions: a gateway turn quoted at Anthropic's list
// price because settings.json here says anthropic, or a real bill shown as
// nothing at all because the local setting is a gateway. A price is only
// allowed for a model uhai ships an endpoint for, and *which* model that is
// has to be the one that answered.
func (m *teaModel) pricingModel() string {
	if attached != nil {
		return attachedModel
	}
	if m.agent.Provider == nil {
		return ""
	}
	return m.agent.Provider.Name()
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
		return teaToolMsg(toolLine(e.Text, e.Input))
	case daemon.EventNotice:
		// A note, not an answer. As an answer it went through glamour, which
		// is wrong for a dim one-liner — and teaTextMsg clears the stream,
		// so a task reporting in mid-turn wiped the answer being written.
		return teaNoteMsg(e.Text)
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
		m.program.Send(teaNoteMsg("that question was already settled: " + err.Error()))
	}
}

// askDaemon runs one turn on the daemon and returns when it is over.
func (m *teaModel) askDaemon(ctx context.Context, prompt string) tea.Msg {
	return teaDoneMsg{err: attached.Prompt(ctx, prompt)}
}

// contextFill is the left half of the status row: how much of the window the
// conversation has taken, so compaction is something you saw coming.
//
// Attached it read the local agent, whose history is empty — so a daemon
// holding 59 messages against a 32k window, half full and a few turns from
// being summarised, was drawn as "ctx 4%": the size of the system prompt and
// nothing else. It is the one indicator whose whole purpose is warning, and
// it was reassuring instead.
//
// The tokens are the provider's own count of the last request, which is a
// better number than the estimate the local path uses and is already crossing
// the socket with every usage event.
//
// ponytail: no percentage attached, because the window is the daemon's and it
// is not on the wire — health reports the model it was built with, not the
// one a gateway may have routed to since. Put the daemon's MaxContextTokens on
// health and the percentage comes back; it needs a lock first, since OnModel
// writes it from the turn.
func (m *teaModel) contextFill() string {
	if attached != nil {
		if m.usage.Input == 0 {
			return ""
		}
		return "ctx " + fmtTokens(m.usage.Input)
	}
	limit := m.agent.MaxContextTokens
	if limit <= 0 {
		return ""
	}
	if used := m.agent.Tokens() * 100 / limit; used > 0 {
		return fmt.Sprintf("ctx %d%%", used)
	}
	return ""
}
