package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
)

const maxInputLines = 5

// promptHistory is how many past prompts the arrows walk back through.
const promptHistory = 5

type teaModel struct {
	agent     *agent.Agent
	input     textarea.Model
	chat      viewport.Model
	spinner   spinner.Model
	renderer  *glamour.TermRenderer
	lines     []chatEntry
	width     int
	height    int
	busy      bool
	status    string
	confirm   *teaConfirm
	mode      teaMode
	picker    list.Model
	keyInput  textarea.Model
	credField string // which credential the entry box is collecting

	// thinking is a model's working out while it arrives, and thinkStart is
	// when it began. It is shown live and then collapsed to one line: it is
	// how the answer was reached, not the answer, and on some models there is
	// more of it than there is answer.
	thinking    string
	thinkStart  time.Time
	provider    string
	commandSel  int
	inputHeight int
	started     time.Time
	usage       provider.Usage

	// stream is the answer as it arrives, shown under the history until the
	// complete text replaces it. streamed is its length in characters, which
	// is what moves the token figure while a call is still running: the real
	// count only comes with the last chunk, which is also when the call ends.
	stream   string
	streamed int

	// queued is a prompt typed while the model was still working. It is sent
	// as soon as the turn ends, so enter never throws the text away.
	queued string

	// banner is whether the welcome box has been printed; it waits for the
	// first size message, since it is drawn to the terminal's width.
	banner bool

	// chatted is whether anything has been said yet, so the first question
	// does not open with a blank line under the welcome box.
	chatted bool

	// cancel stops the turn in flight; nil when nothing is running.
	cancel context.CancelFunc

	// wheel is whether the app has asked for mouse reports. It has to ask for
	// the wheel to reach it at all, and that is the same thing that stops a
	// plain drag selecting text — which the terminals hand back when shift is
	// held, so this is on by default and /mouse hands the mouse over whole.
	wheel bool

	// toolCalls counts what the model reached for during this turn, for the
	// line that closes it.
	toolCalls int

	// notes are reports from background work, waiting to travel with the next
	// prompt so the model learns how it went without anyone retyping it.
	notes []string

	// opening is what has to be said once the welcome box is up: a resumed
	// conversation, or why there is no provider yet.
	opening []string

	// allowed is the tools the user said "always" to, for the rest of this
	// session. It is written when the answer comes in and read from the
	// agent's goroutine when the next tool call arrives, so it carries its
	// own lock. The settings' allow list is read per command instead of being
	// copied in here: editing it should take effect at once.
	allowed *allowSet

	// saveFailed keeps the warning about an unsaveable session to one.
	saveFailed bool

	// past is the last few prompts, oldest first, walked with the arrows.
	// browsing is where in it the arrows are, len(past) when not browsing,
	// and draft is what was typed before the walk started.
	past     []string
	browsing int
	draft    string
}

type teaMode int

const (
	teaPrompt teaMode = iota
	teaProviderPicker
	teaKeyEntry
	teaModelPicker
	teaSkillPicker
)

// chatEntry is one thing said, kept as it was written. What it looks like
// depends on the width, so it is rendered on demand and the result cached —
// a resize just drops the cache.
type chatEntry struct {
	kind     entryKind
	text     string
	rendered string
}

type entryKind int

const (
	entryPlain  entryKind = iota // already styled, and no wider than it needs
	entryAsk                     // a question of the user's, on its own band
	entryAnswer                  // markdown from the model
	entryBanner                  // the welcome box
)

type teaItem struct{ title, desc string }

func (i teaItem) FilterValue() string { return i.title }

func (i teaItem) Title() string { return i.title }

func (i teaItem) Description() string { return i.desc }

type teaTextMsg string

type teaDeltaMsg string

// teaThinkMsg is a piece of the model's working out.
type teaThinkMsg string

type teaToolMsg string

type teaDoneMsg struct{ err error }

type teaCompactMsg struct {
	before, after int
	err           error
}

type teaUsageMsg provider.Usage

type teaConfirm struct {
	name  string
	input string
	reply chan bool

	// choice is which answer is under the cursor, so the question can be
	// answered by looking at it rather than by remembering a letter.
	choice int
}

type teaConfirmMsg struct{ request teaConfirm }

func newTeaModel(a *agent.Agent, startupErr error) *teaModel {
	input := textarea.New()
	input.Placeholder = "Ask uhai anything..."
	input.Prompt = "❯ "
	input.CharLimit = 0
	input.ShowLineNumbers = false
	input.SetHeight(1)
	input.SetPromptFunc(2, func(lineIndex int) string {
		if lineIndex == 0 {
			return "❯ "
		}
		return "  "
	})
	applyInputStyle(&input)
	input.Focus()
	keyInput := textarea.New()
	keyInput.CharLimit = 0
	keyInput.Prompt = ""
	keyInput.ShowLineNumbers = false
	keyInput.SetHeight(1)
	applyInputStyle(&keyInput)
	m := &teaModel{
		agent:       a,
		input:       input,
		spinner:     spinner.New(),
		renderer:    newRenderer(0),
		keyInput:    keyInput,
		chat:        newChatViewport(),
		inputHeight: 1,
		wheel:       true,
		allowed:     &allowSet{},
	}
	// Held until the welcome box has been drawn, which needs a width, so that
	// these read as notes under it rather than as something above it.
	if n := len(a.History); n > 0 {
		m.opening = append(m.opening, teaDim.Render(fmt.Sprintf(
			"resumed %d messages, ~%s tokens — /clear to start fresh", n, fmtTokens(a.Tokens()))))
	}
	if startupErr != nil {
		m.opening = append(m.opening, teaDim.Render(startupErr.Error()))
	}
	return m
}

// addHistory adds one entry to the chat. The app owns the scrolling now, so
// that the prompt underneath can stay where it is while the chat moves.
func (m *teaModel) addHistory(text string) {
	m.add(chatEntry{kind: entryPlain, text: text})
}

func (m *teaModel) add(e chatEntry) {
	m.lines = append(m.lines, e)
	m.refresh()
}

// refresh redraws the chat with the answer currently streaming at its end.
// It follows the bottom only while the reader is already there, so scrolling
// back is not undone by the next chunk.
func (m *teaModel) refresh() {
	follow := m.chat.AtBottom()
	rows := make([]string, 0, len(m.lines)+1)
	for i, e := range m.lines {
		if e.rendered == "" {
			m.lines[i].rendered = m.render(e)
		}
		// Wrapped here, not left to the terminal: the viewport counts lines,
		// so a line the terminal draws as two would push the bottom of the
		// chat under the prompt and keep it there.
		for _, line := range strings.Split(m.lines[i].rendered, "\n") {
			rows = append(rows, wrapHanging(line, m.cols())...)
		}
	}
	rows = append(rows, m.streamRows()...)
	m.chat.SetContent(strings.Join(rows, "\n"))
	if follow {
		m.chat.GotoBottom()
	}
}

func (m *teaModel) Init() tea.Cmd { return textarea.Blink }

func (m *teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width != m.width {
			m.renderer = newRenderer(msg.Width - 4)
			for i := range m.lines {
				m.lines[i].rendered = "" // rendered for the old width
			}
		}
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(1, msg.Width-4))
		m.chat.Width = m.cols()
		m.resizeInput()
		if !m.banner {
			m.banner = true
			m.add(chatEntry{kind: entryBanner, text: providerLabel(m.agent.Provider)})
			for _, line := range m.opening {
				m.addHistory(line)
			}
			m.opening = nil
		}
		m.refresh()
	case tea.KeyMsg:
		if m.confirm != nil {
			return m.updateConfirm(msg)
		}
		if m.mode != teaPrompt {
			return m.updatePicker(msg)
		}
		commands := matches(m.input.Value())
		if len(commands) > 0 {
			switch msg.String() {
			case "up":
				m.commandSel = (m.commandSel - 1 + len(commands)) % len(commands)
				return m, nil
			case "down":
				m.commandSel = (m.commandSel + 1) % len(commands)
				return m, nil
			case "tab":
				m.input.SetValue(commands[m.commandSel].name)
				return m, nil
			case "enter":
				m.input.SetValue(commands[m.commandSel].name)
				if !m.busy {
					return m, m.submit()
				}
			}
		}
		switch msg.String() {
		case "up", "down":
			// The prompts already sent, walked the way a shell walks them. A
			// prompt several lines tall keeps the arrows for its own cursor;
			// the chat scrolls with shift and the arrows, or the page keys.
			if m.inputHeight == 1 {
				m.recall(msg.String() == "up")
				return m, nil
			}
		case "ctrl+y", "ctrl+e":
			// Plain control characters, the pair vim scrolls with: nothing —
			// not the terminal, not tmux — gets to translate them, so this
			// works where shift with the arrows never arrives.
			if msg.String() == "ctrl+y" {
				m.chat.HalfViewUp()
			} else {
				m.chat.HalfViewDown()
			}
			return m, nil
		case "pgup", "pgdown", "ctrl+p", "ctrl+n":
			// On a Mac the page keys are fn with the arrows, so they walk the
			// prompts too rather than being a second way to scroll.
			m.recall(msg.String() == "pgup" || msg.String() == "ctrl+p")
			return m, nil
		case "esc":
			if m.busy && m.cancel != nil {
				m.cancel()
			}
		case "ctrl+c", "ctrl+d":
			if !m.busy {
				return m, tea.Quit
			}
			if m.cancel != nil {
				m.cancel()
			}
		case "enter":
			if !m.busy {
				return m, m.submit()
			}
			m.queued = strings.TrimSpace(m.input.Value())
			m.input.Reset()
			return m, nil
		}
	case teaThinkMsg:
		if m.thinking == "" {
			m.thinkStart = time.Now()
		}
		m.thinking += string(msg)
		m.refresh()
	case teaDeltaMsg:
		// The answer starting is what ends the thinking: what it was working
		// towards is here, and the working out collapses to a line.
		m.collapseThinking()
		m.streamed += len(msg)
		m.stream += string(msg)
		m.refresh()
	case teaTextMsg:
		// The complete text arrives once the call is done: the streamed copy
		// makes way for the rendered one.
		m.collapseThinking()
		m.stream = ""
		m.add(chatEntry{kind: entryAnswer, text: string(msg)})
	case teaToolMsg:
		m.toolCalls++
		// Cut to the width rather than to some number of characters: a tool
		// call is a note of what happened, and a note that wraps twice reads
		// as the thing itself.
		// What is left after the marker (4), the hanging indent the wrap
		// reserves (2), and the ellipsis truncate adds (1).
		m.addHistory(teaDim.Render("  ⎿ " + truncate(string(msg), max(20, m.cols()-7))))
	case teaConfirmMsg:
		// The question takes over the block below the chat, so what is about
		// to happen is on screen while it is being decided rather than
		// scrolled past in one line.
		m.confirm = &msg.request
	case teaNoteMsg:
		m.addHistory(teaDim.Render(string(msg)))
	case teaModelsMsg:
		m.status = ""
		if msg.err != nil || len(msg.items) == 0 {
			m.mode = teaPrompt
			m.addHistory(teaDim.Render("could not list models from connected providers"))
			return m, nil
		}
		m.picker = m.newPicker(msg.items, "Choose model")
		m.mode = teaModelPicker
	case teaUsageMsg:
		// Input is the whole prompt each call, so it replaces; output adds up.
		// The real count supersedes the estimate for the call that just ended.
		m.usage.Input = msg.Input
		m.usage.Output += msg.Output
		// The turn's shape above, the conversation's bill below: they are not
		// the same sum, since every call in a turn is charged for its input.
		recordUsage(provider.Usage(msg))
		m.streamed = 0
	case teaCompactMsg:
		m.busy, m.cancel, m.status = false, nil, ""
		if msg.err != nil {
			m.addHistory(teaDim.Render("could not compact: " + msg.err.Error()))
			break
		}
		m.addHistory(teaDim.Render(fmt.Sprintf("history compacted: %s → %s tokens",
			fmtTokens(msg.before), fmtTokens(msg.after))))
	case teaDoneMsg:
		m.busy = false
		m.cancel = nil
		if m.stream != "" { // the answer was cut short: keep what arrived
			text := m.stream
			m.stream = ""
			m.addHistory(text)
		}
		m.status = ""
		m.collapseThinking() // a turn that died mid-thought still says it thought
		m.afterTurn()
		// However a turn ended, the line saying so is about the turn rather
		// than part of what was said, so it stands clear of it.
		stopped := errors.Is(msg.err, context.Canceled)
		m.addHistory("")
		switch {
		case stopped:
			m.addHistory(teaDim.Render("interrupted"))
		case msg.err != nil:
			m.addHistory(teaDim.Render("error: " + msg.err.Error()))
			// A failed turn stops there, and the next move is not obvious:
			// nothing says whether the reading it already did was lost with
			// it. It was not — the turn is closed in the history, tool
			// results and all — so sending the prompt again carries on from
			// there rather than starting over.
			m.addHistory(teaDim.Render("↑ brings the prompt back — what it already read is still in the history"))
		default:
			m.addHistory(m.turnSummary())
		}
		if m.queued != "" {
			queued := m.queued
			m.queued = ""
			// After an interrupt the queued prompt goes back in the box: the
			// user stopped the model, firing the next turn at them is not it.
			if stopped {
				m.input.SetValue(queued)
				return m, nil
			}
			m.input.SetValue(queued)
			return m, m.submit()
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.chat, _ = m.chat.Update(msg)
	if m.confirm == nil {
		before := m.input.Value()
		m.input, cmd = m.input.Update(msg)
		if before != m.input.Value() {
			m.commandSel = 0
		}
		m.resizeInput()
	}
	return m, cmd
}

// teaNoteMsg is a dim line from work that finished after the command did.
type teaNoteMsg string

type teaModelsMsg struct {
	items []list.Item
	err   error
}

// menuLimit is how many command rows are shown at once. The menu used to draw
// every match without being counted in the block, so eleven commands made the
// view taller than the terminal and the rows that fell off the bottom took the
// selection with them: pressing up at the top wrapped to /mouse, which was no
// longer on the screen.
const menuLimit = 8

// confirmChoices are the answers, in the order they are offered.
const (
	choiceYes = iota
	choiceAlways
	choiceNo
)

func runTea(a *agent.Agent, startupErr error) error {
	m := newTeaModel(a, startupErr)
	// The alt screen is what pins the prompt: the app owns every row, so the
	// chat can scroll under a form that stays put. That leaves no scrollback
	// for the terminal to scroll, so the wheel has to reach the app itself —
	// and asking for it is the only way, since tmux otherwise swallows the
	// wheel into its own copy mode. Selecting text then needs Shift held
	// (Option in iTerm2 and Terminal.app), or tmux's copy mode.
	// The alternate screen is what pins the prompt: the app owns every row,
	// so the chat scrolls under a form that stays put.
	//
	// Alternate scroll would turn the wheel into arrow keys, and the arrows
	// are the prompt's own history — scrolling would rewrite what is in the
	// box. It stays off, and the chat is scrolled with shift and the arrows
	// or the page keys. (tmux 3.6 dropped the mode anyway, so inside tmux the
	// wheel never reaches an application on the alternate screen.) Written
	// before the program starts, so it cannot race the renderer, and put back
	// on the way out.
	fmt.Fprint(os.Stdout, "\x1b[?1007l")
	defer fmt.Fprint(os.Stdout, "\x1b[?1007h")

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Again once the alternate screen is up: terminals that keep their modes
	// per screen restore the old one when bubbletea switches. Setting a mode
	// moves no cursor and prints nothing, so it is safe to write from here
	// while the renderer draws.
	go func() {
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(os.Stdout, "\x1b[?1007l")
	}()

	a.OnText = func(text string) { p.Send(teaTextMsg(text)) }
	a.OnToolCall = func(name, input string) { p.Send(teaToolMsg(toolLine(name, input))) }
	a.OnUsage = func(u provider.Usage) { p.Send(teaUsageMsg(u)) }
	a.OnDelta = func(delta string) { p.Send(teaDeltaMsg(delta)) }
	a.OnReasoning = func(delta string) { p.Send(teaThinkMsg(delta)) }
	a.Confirm = func(name, input string) bool {
		switch m.decide(name, input) {
		case config.PermAllow:
			return true
		case config.PermDeny:
			// Refused by the project, so nobody is asked and the model is
			// told plainly rather than left to guess at a silent failure.
			p.Send(teaTextMsg(teaDim.Render("refused by this project's settings: " + name)))
			return false
		}
		reply := make(chan bool, 1)
		p.Send(teaConfirmMsg{request: teaConfirm{name: name, input: input, reply: reply}})
		return <-reply
	}
	_, err := p.Run()
	return err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
