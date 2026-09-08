// The prompt itself: what happens when a line is submitted, what the arrows
// recall, what closes a turn, and how a permission question is answered.
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
)

// afterTurn keeps the conversation recoverable and announces background work
// that ended while the model was busy. Both belong between turns: a task
// finishing cannot paint the screen itself.
func (m *teaModel) afterTurn() {
	if err := SaveSession(m.agent); err != nil && !m.saveFailed {
		m.saveFailed = true // said once; losing the history quietly would be worse
		m.addHistory(teaDim.Render("the session is not being saved: " + err.Error()))
	}
	m.collectFinished()
}

// collectFinished announces background work that has ended, to the user now
// and to the model with its next prompt.
func (m *teaModel) collectFinished() {
	for _, t := range finishedTasks(m.agent) {
		m.addHistory(finishedLine(t))
		m.notes = append(m.notes, taskNote(t))
	}
}

// recall walks the past prompts, oldest last. Stepping past the newest one
// brings back whatever was being typed when the walk started.
func (m *teaModel) recall(back bool) {
	if len(m.past) == 0 {
		return
	}
	if m.browsing == len(m.past) {
		if !back {
			return
		}
		m.draft = m.input.Value()
	}
	switch {
	case back && m.browsing > 0:
		m.browsing--
	case !back && m.browsing < len(m.past):
		m.browsing++
	default:
		return
	}
	if m.browsing == len(m.past) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.past[m.browsing])
	}
	m.input.CursorEnd()
	m.resizeInput()
}

// remember keeps the last few prompts for the arrows. A prompt repeated is
// not worth a second slot.
func (m *teaModel) remember(value string) {
	if len(m.past) == 0 || m.past[len(m.past)-1] != value {
		m.past = append(m.past, value)
	}
	if len(m.past) > promptHistory {
		m.past = m.past[len(m.past)-promptHistory:]
	}
	m.browsing, m.draft = len(m.past), ""
}

func (m *teaModel) submit() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	if value == "" {
		return nil
	}
	m.remember(value)
	if m.chatted {
		m.addHistory("") // a turn of its own, not another line in a wall of text
	}
	m.chatted = true
	m.add(chatEntry{kind: entryAsk, text: value})
	m.addHistory("") // the answer starts a line below the question, not against it

	if strings.HasPrefix(value, "/") {
		return m.slashCommand(value)
	}
	if m.agent.Provider == nil {
		m.addHistory(teaDim.Render("no provider connected — type /connect"))
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	// Anything that finished while the prompt was being typed travels with it.
	m.collectFinished()
	if len(m.notes) > 0 {
		m.addHistory(teaDim.Render(fmt.Sprintf("(sending %d background report(s) with this)", len(m.notes))))
		value = strings.Join(m.notes, "\n\n") + "\n\n" + value
		m.notes = nil
	}

	m.busy = true
	m.started = time.Now()
	m.usage = provider.Usage{}
	m.toolCalls = 0
	m.stream = ""
	m.streamed = 0
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		defer cancel()
		if attached != nil {
			return m.askDaemon(ctx, value)
		}
		return teaDoneMsg{err: m.agent.Ask(ctx, value)}
	})
}

// slashCommand answers a line beginning with "/". It is apart from submit
// because a command and a question share nothing past the slash: one is
// answered here and now, the other starts a turn.
func (m *teaModel) slashCommand(value string) tea.Cmd {
	switch {
	case value == "/exit" || value == "/quit":
		return tea.Quit
	case value == "/clear":
		m.agent.History = nil
		m.lines = nil // not even the echo of /clear survives the clearing
		m.chatted = false
		// A cleared chat is a session starting over, so it opens the way one
		// does: with the welcome box, not with an empty screen.
		m.add(chatEntry{kind: entryBanner, text: m.answeringModel()})
		return nil
	case value == "/compact":
		if m.agent.Provider == nil {
			m.addHistory(teaDim.Render("no provider connected — type /connect"))
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		m.busy = true
		m.started = time.Now()
		m.status = "compacting..."
		before := m.agent.Tokens()
		return tea.Batch(m.spinner.Tick, func() tea.Msg {
			defer cancel()
			err := m.agent.Compact(ctx)
			return teaCompactMsg{before: before, after: m.agent.Tokens(), err: err}
		})
	case strings.HasPrefix(value, "/cost"):
		if report := spentReport(m.agent.Provider); report != "" {
			m.addHistory(teaDim.Render(report))
		} else {
			m.addHistory(teaDim.Render("nothing asked yet this session"))
		}
		return nil
	case strings.HasPrefix(value, "/skills"):
		return m.beginSkills()
	case strings.HasPrefix(value, "/tasks"):
		m.addHistory(tasksReport(m.agent, strings.TrimSpace(strings.TrimPrefix(value, "/tasks"))))
		return nil
	case strings.HasPrefix(value, "/stop"):
		m.addHistory(stopTask(m.agent, strings.TrimSpace(strings.TrimPrefix(value, "/stop"))))
		return nil
	case strings.HasPrefix(value, "/check"):
		command, err := spawnCheck(m.agent, strings.TrimSpace(strings.TrimPrefix(value, "/check")))
		if err != nil {
			m.addHistory(teaDim.Render(err.Error()))
			return nil
		}
		m.addHistory(teaDim.Render("running " + command + " — /tasks to check on it"))
		return nil
	case strings.HasPrefix(value, "/bg"):
		prompt := strings.TrimSpace(strings.TrimPrefix(value, "/bg"))
		if err := spawnBackground(m.agent, prompt); err != nil {
			m.addHistory(teaDim.Render(err.Error()))
			return nil
		}
		m.addHistory(teaDim.Render("started in the background — /tasks to check on it"))
		return nil
	case value == "/mouse":
		m.wheel = !m.wheel
		if m.wheel {
			m.addHistory(teaDim.Render("mouse: the wheel scrolls the chat — hold shift to select text"))
			return nil
		}
		m.addHistory(teaDim.Render("mouse: handed to the terminal — plain drag selects, ^y/^e scroll"))
		return nil
	case value == "/help":
		m.addHistory(helpText())
		return nil
	case strings.HasPrefix(value, "/model"):
		arg := strings.TrimSpace(strings.TrimPrefix(value, "/model"))
		if arg == "" {
			return m.beginModels()
		}
		// "refresh" cannot be a model — those are written provider/model — so
		// there is nothing to be ambiguous with. It exists because the six
		// hour cache is a courtesy until the moment you have just added a
		// provider to a gateway, and then it is a wall with no door.
		if arg == "refresh" {
			if err := config.ForgetModelLists(); err != nil {
				m.addHistory(teaDim.Render("could not clear the cache: " + err.Error()))
				return nil
			}
			return m.beginModels()
		}
		return m.changeModel(arg, true)
	case strings.HasPrefix(value, "/disconnect"):
		return m.disconnect(strings.TrimSpace(strings.TrimPrefix(value, "/disconnect")))

	case strings.HasPrefix(value, "/connect"):
		return m.beginConnect(strings.TrimSpace(strings.TrimPrefix(value, "/connect")))
	case strings.HasPrefix(value, "/"):
		m.addHistory(teaDim.Render("unknown command: " + value))
		return nil
	}
	return nil
}

// helpText is the whole of /help. It sat inside the command switch and was
// most of its length — fifty lines of string in the middle of a dispatch,
// which made the dispatch look like the hard part when it is a list.
func helpText() string {
	return teaTitle.Render("commands") + `
  /connect  connect a provider
  /model    choose a model
  /check    run the project's tests as a task, or /check <command>
  /stop     stop a task: /stop t1
  /compact  summarize the history to free up context
  /cost     what this conversation has cost so far
  /skills   the skills in play — enter switches one off or on
  /tasks    list tasks, or /tasks t1 to read one's report
  /bg       run a prompt in the background, read-only
  /mouse    hand the mouse back to the terminal
  /clear    clear chat
  /exit     quit

` + teaTitle.Render("keys") + `
  esc            stop the model
  ↑ ↓            recall past prompts (pgup/pgdn and ^p/^n too)
  ^y ^e          scroll the chat (shift+↑↓ too)
  enter          send — or queue, while the model is working

` + teaTitle.Render("mouse") + `
  wheel          scrolls the chat
  shift+drag     selects text — in tmux this spans the panes, since the
                 terminal knows nothing about splits
  in tmux        prefix + [ then drag: selects inside the pane, and copies

` + teaTitle.Render("what it can do") + `
  reads freely   read_file, glob, grep — no question asked
  asks first     write_file, edit_file, run_bash — the question shows the
                 diff, or the command itself, so it can be judged
  in the back    /bg runs a prompt read-only, /check runs the tests; both
                 report into the chat and into the model's next prompt
  allow it once  a at the question allows that tool for this session only;
                 .uhai/settings.json "permissions" makes it permanent

` + teaTitle.Render("outside this session") + `
  uhai -p "..."      one answer and exit — and a pipe works: cat q | uhai
  uhai -sessions     the conversations saved so far
  uhai -resume [id]  carry on with one, on the model it was held with
  AGENTS.md           this project's own instructions, read every prompt —
                      CLAUDE.md and UHAI.md are read the same way
  .claude/skills/     instructions for particular jobs, opened when needed`
}

func (m *teaModel) confirmChoices() []string {
	return []string{
		"Yes",
		"Yes, and don't ask again for " + m.confirm.name + " this session",
		"No, and tell uhai what to do instead",
	}
}

func (m *teaModel) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	choices := m.confirmChoices()

	switch key := msg.String(); key {
	case "up":
		m.confirm.choice = (m.confirm.choice - 1 + len(choices)) % len(choices)
		return m, nil
	case "down", "tab":
		m.confirm.choice = (m.confirm.choice + 1) % len(choices)
		return m, nil
	case "1", "2", "3":
		m.confirm.choice = int(key[0] - '1')
		return m, m.answerConfirm()
	case "enter":
		return m, m.answerConfirm()
	case "y":
		m.confirm.choice = choiceYes
		return m, m.answerConfirm()
	case "a":
		m.confirm.choice = choiceAlways
		return m, m.answerConfirm()
	case "n", "esc", "ctrl+c":
		m.confirm.choice = choiceNo
		return m, m.answerConfirm()
	}
	return m, nil
}

// answerConfirm hands the decision back to the waiting tool call.
func (m *teaModel) answerConfirm() tea.Cmd {
	if m.confirm.choice == choiceAlways {
		m.allowed.add(m.confirm.name)
		m.addHistory(teaDim.Render(m.confirm.name + " is allowed for the rest of this session"))
	}
	m.confirm.reply <- m.confirm.choice != choiceNo
	m.confirm = nil
	m.status = ""
	return nil
}
