package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/didik-prabowo/ouhai/internal/agent"
	"github.com/didik-prabowo/ouhai/internal/config"
	"github.com/didik-prabowo/ouhai/internal/provider"
)

const maxInputLines = 5

// promptHistory is how many past prompts the arrows walk back through.
const promptHistory = 5

type teaModel struct {
	agent       *agent.Agent
	input       textarea.Model
	chat        viewport.Model
	spinner     spinner.Model
	renderer    *glamour.TermRenderer
	lines       []chatEntry
	width       int
	height      int
	busy        bool
	status      string
	confirm     *teaConfirm
	mode        teaMode
	picker      list.Model
	keyInput    textarea.Model
	credField   string // which credential the entry box is collecting
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

func (m *teaModel) render(e chatEntry) string {
	switch e.kind {
	case entryAsk:
		return teaAsk.Width(m.cols()).Render("❯ " + e.text)
	case entryAnswer:
		return m.rendererText(e.text)
	case entryBanner:
		return strings.Join(boxLines(m.cols(),
			accentAt+"✻"+reset+" Welcome to ouhai!  "+dim+builtAt()+reset,
			dim+"provider:"+reset+" "+e.text,
			dim+"cwd:"+reset+" "+workingDir(),
			dim+"type / for commands, /exit to quit"+reset,
		), "\n")
	}
	return e.text
}

type teaItem struct{ title, desc string }

func (i teaItem) FilterValue() string { return i.title }
func (i teaItem) Title() string       { return i.title }
func (i teaItem) Description() string { return i.desc }

type teaTextMsg string
type teaDeltaMsg string
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
	input.Placeholder = "Ask ouhai anything..."
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

// newChatViewport is the scrolling chat. Its default keymap steals j, k, f,
// b, u, d and space, which are ordinary typing here, so it keeps only the
// keys the prompt has no use for; the arrows are dealt with in update, where
// it is known whether the prompt itself wants them.
func newChatViewport() viewport.Model {
	v := viewport.New(80, 20)
	// Shift with the arrows is the only thing that scrolls: it reaches the app
	// whatever the terminal thinks about wheels, and nothing else wants it.
	// The page keys are the prompt's history, like the arrows.
	v.KeyMap = viewport.KeyMap{
		Up:   key.NewBinding(key.WithKeys("shift+up")),
		Down: key.NewBinding(key.WithKeys("shift+down")),
	}
	return v
}

func applyInputStyle(input *textarea.Model) {
	focused, blurred := textarea.DefaultStyles()
	for _, style := range []*textarea.Style{&focused, &blurred} {
		style.Base = lipgloss.NewStyle()
		style.CursorLine = lipgloss.NewStyle()
		style.EndOfBuffer = lipgloss.NewStyle()
		style.Text = lipgloss.NewStyle().Foreground(text)
		style.Prompt = lipgloss.NewStyle().Foreground(accent)
		style.Placeholder = lipgloss.NewStyle().Foreground(muted)
	}
	input.FocusedStyle = focused
	input.BlurredStyle = blurred
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
	case teaDeltaMsg:
		m.streamed += len(msg)
		m.stream += string(msg)
		m.refresh()
	case teaTextMsg:
		// The complete text arrives once the call is done: the streamed copy
		// makes way for the rendered one.
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

func (m *teaModel) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Key entry is read before the general escape below: backing out of a
	// provider that already has a key means keeping that key, not abandoning
	// the connection.
	if m.mode == teaKeyEntry {
		switch msg.String() {
		case "esc":
			m.keyInput.Reset()
			if m.credField != config.FieldKey {
				return m, m.activateProvider(m.provider) // the key is in already
			}
			if config.APIKey(m.provider) == "" {
				m.mode = teaPrompt
				m.input.Focus()
				return m, nil
			}
			return m, m.activateProvider(m.provider)
		case "enter":
			return m, m.saveCredential(strings.TrimSpace(m.keyInput.Value()))
		}
		var cmd tea.Cmd
		m.keyInput, cmd = m.keyInput.Update(msg)
		return m, cmd
	}
	if msg.String() == "esc" {
		m.mode = teaPrompt
		m.input.Focus()
		return m, nil
	}
	if msg.String() == "enter" {
		item := m.picker.SelectedItem()
		if item == nil {
			return m, nil
		}
		selected := item.(teaItem).title
		if m.mode == teaProviderPicker {
			return m, m.selectProvider(selected)
		}
		return m, m.changeModel(selected)
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	return m, cmd
}

func (m *teaModel) beginConnect(arg string) tea.Cmd {
	if arg != "" {
		return m.selectProvider(arg)
	}
	items := make([]list.Item, 0)
	for _, item := range providerItems() {
		items = append(items, teaItem{title: item.name, desc: item.desc})
	}
	m.picker = m.newPicker(items, "Connect provider")
	m.mode = teaProviderPicker
	return nil
}

// askCredential puts the entry box in front of one field. Providers are asked
// for their key first and their extras after, one question at a time, because
// a form that asks for three things at once has to explain all three before
// any of them can be typed.
func (m *teaModel) askCredential(field string) {
	m.credField = field
	m.keyInput.Reset()
	saved := config.Get(m.provider)[field] != ""
	switch {
	case field == config.FieldKey && saved:
		m.keyInput.Placeholder = "paste a new API key, or esc to keep the one saved"
	case field == config.FieldKey:
		m.keyInput.Placeholder = "paste API key"
	case saved:
		m.keyInput.Placeholder = "enter keeps the one saved"
	default:
		m.keyInput.Placeholder = "enter to skip — only identity-linked keys need one"
	}
	m.keyInput.Focus()
	m.mode = teaKeyEntry
}

// saveCredential stores what was typed, then asks for the next field or
// connects. An empty answer is only allowed for the extras: a provider with no
// key cannot be connected at all, so an empty one leaves the question up.
func (m *teaModel) saveCredential(value string) tea.Cmd {
	if value == "" && m.credField == config.FieldKey {
		return nil
	}
	if value != "" {
		if err := config.Save(m.provider, config.Creds{m.credField: value}); err != nil {
			m.addHistory(teaDim.Render("could not save " + credLabel(m.credField) + ": " + err.Error()))
			m.mode = teaPrompt
			m.input.Focus()
			return nil
		}
		// The environment beats the file, so saving while one is exported
		// looks like nothing happened.
		if env := config.EnvVar(m.provider, m.credField); env != "" {
			m.addHistory(teaDim.Render(env + " is set and wins over what is saved — unset it for this to take effect"))
		}
	}
	m.keyInput.Reset()
	if m.credField == config.FieldKey {
		if extra := config.ExtraFields(m.provider); len(extra) > 0 {
			m.askCredential(extra[0])
			return nil
		}
	}
	return m.activateProvider(m.provider)
}

// credLabel is what a credential is called on screen.
func credLabel(field string) string {
	if field == config.FieldWorkspace {
		return "workspace id"
	}
	return "API key"
}

func (m *teaModel) selectProvider(name string) tea.Cmd {
	if !config.Known(name) {
		m.addHistory(teaDim.Render("unknown provider: " + name))
		m.mode = teaPrompt
		return nil
	}
	// Always offered, even when a key is saved: a key that is refused is
	// exactly the one you need to replace, and there was no way to.
	if config.NeedsKey(name) {
		m.provider = name
		m.askCredential(config.FieldKey)
		return nil
	}
	return m.activateProvider(name)
}

func (m *teaModel) activateProvider(name string) tea.Cmd {
	modelName := config.DefaultModel(name)
	if modelName == "" {
		m.addHistory(teaDim.Render("no default model configured for " + name))
		m.mode = teaPrompt
		return nil
	}
	if err := config.SaveModel(name + "/" + modelName); err != nil {
		m.addHistory(teaDim.Render("could not save model: " + err.Error()))
		m.mode = teaPrompt
		return nil
	}
	p, err := config.LoadProvider()
	if err != nil {
		m.addHistory(teaDim.Render("could not connect: " + err.Error()))
		m.mode = teaPrompt
		return nil
	}
	m.useProvider(p)
	m.mode = teaPrompt
	m.input.Focus()
	m.addHistory(teaDim.Render("connected to " + p.Name()))
	return nil
}

// useProvider switches model, and with it how much history fits before the
// agent has to summarize — a window is a property of the model, not a setting.
func (m *teaModel) useProvider(p provider.Provider) {
	m.agent.Provider = p
	m.agent.MaxContextTokens = config.ContextWindow(p.Name())
	m.agent.UseTools = config.SupportsTools(p.Name())
}

func (m *teaModel) beginModels() tea.Cmd {
	m.status = m.spinner.View() + " loading models..."
	return func() tea.Msg {
		items, err := loadModelItems()
		return teaModelsMsg{items: items, err: err}
	}
}

type teaModelsMsg struct {
	items []list.Item
	err   error
}

func loadModelItems() ([]list.Item, error) {
	var items []list.Item
	for _, name := range config.ConnectedProviders() {
		modelName := config.DefaultModel(name)
		if modelName == "" {
			continue
		}
		p, err := config.LoadProviderFor(name + "/" + modelName)
		if err != nil {
			continue
		}
		lister, ok := p.(provider.ModelLister)
		if !ok {
			continue
		}
		models, err := lister.Models()
		if err != nil {
			continue
		}
		for i, modelName := range models {
			if i >= maxRecommendedModelsPerProvider {
				break
			}
			setting := name + "/" + modelName
			items = append(items, teaItem{title: setting, desc: config.ModelSummary(setting)})
		}
	}
	return items, nil
}

// workingHint is the "(12s · ↑1.2k ↓340 tokens)" tail on the thinking line.
// Tokens only appear once a provider call has reported them.
func (m *teaModel) workingHint() string {
	hint := fmtElapsed(time.Since(m.started))
	// Four characters per token, the same rule of thumb agent.Tokens uses.
	out := m.usage.Output + m.streamed/4
	if m.usage.Input > 0 || out > 0 {
		hint += fmt.Sprintf(" · ↑%s ↓%s tokens", fmtTokens(m.usage.Input), fmtTokens(out))
		if m.agent.Provider != nil {
			if cost := config.CostUSD(m.agent.Provider.Name(), m.usage.Input, out); cost != "" {
				hint += " · " + cost
			}
		}
	}
	return "(" + hint + ")"
}

func fmtElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

// streamRows is the answer as it arrives, wrapped. It is raw text: rendering
// the markdown again on every chunk is not worth it, and the finished answer
// replaces it rendered.
func (m *teaModel) streamRows() []string {
	if m.stream == "" {
		return nil
	}
	var rows []string
	for _, line := range strings.Split(m.stream, "\n") {
		rows = append(rows, wrapHanging(line, m.cols())...)
	}
	return rows
}

func (m *teaModel) rendererText(text string) string {
	rendered, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
	// Glamour pads every line out to the wrap width with styled spaces, which
	// leaves a stiff block of trailing blanks in the scrollback and drags them
	// along when the text is selected. The answer only needs its own width.
	var rows []string
	for _, line := range strings.Split(rendered, "\n") {
		rows = append(rows, strings.TrimRight(line, " \t"))
	}
	return strings.Trim(strings.Join(rows, "\n"), "\n")
}

func (m *teaModel) formSurface(content string) string {
	return teaForm.Width(m.cols()).Render(content)
}

// cols is the width everything drawn here is built to: one column short of
// the terminal. A line that fills the last column makes the terminal wrap it
// on its own, which scrolls the screen and drags the form up a row.
func (m *teaModel) cols() int {
	return max(20, m.width)
}

func (m *teaModel) modelHint() string {
	if m.agent.Provider == nil {
		return "model: -"
	}
	hint := "model: " + m.agent.Provider.Name()
	// How full the window is, so compaction is something you saw coming
	// rather than something that happened to you.
	if limit := m.agent.MaxContextTokens; limit > 0 {
		if used := m.agent.Tokens() * 100 / limit; used > 0 {
			hint = fmt.Sprintf("ctx %d%% · %s", used, hint)
		}
	}
	if width := m.width - 12; width > 0 {
		return truncate(hint, width)
	}
	return hint
}

// builtAt is when the running binary was built, so a session started before
// the last build is obvious rather than mysterious.
func builtAt() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return "built " + info.ModTime().Format("15:04")
}

func workingDir() string {
	dir, _ := os.Getwd()
	return dir
}

// newPicker builds a list that uses the screen it has. The default one is
// generous with space — a blank row between items, a count above them — which
// on a short terminal left a picker showing one model at a time. A list you
// can only see one of is not a list, it is a very slow question.
func (m *teaModel) newPicker(items []list.Item, title string) list.Model {
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0) // the description already separates one item from the next

	picker := list.New(items, delegate, max(20, m.cols()), m.pickerHeight())
	picker.Title = title
	picker.Styles.Title = teaTitle
	picker.SetShowStatusBar(false) // "17 items" spends two rows saying what the list shows
	return picker
}

// pickerHeight is the screen, less the title, the help line and the hint under
// it. Short terminals still get six rows, scrolling for the rest.
func (m *teaModel) pickerHeight() int {
	return max(6, m.height-4)
}

func (m *teaModel) inputWidth() int {
	return max(1, m.width-4)
}

func (m *teaModel) resizeInput() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.input.SetWidth(m.inputWidth())
	lines := m.input.LineInfo().Height
	if lines < 1 {
		lines = len(wrapLine(m.input.Value(), max(1, m.width-6)))
	}
	if lines > maxInputLines {
		lines = maxInputLines
	}
	oldHeight := m.inputHeight
	m.inputHeight = lines
	m.input.SetHeight(lines)
	if oldHeight != lines {
		value := m.input.Value()
		m.input.SetValue(value)
	}
	m.chat.Width = m.cols()
	m.setChatHeight(m.height - m.blockHeight())
}

// setChatHeight resizes the chat and keeps it where it was: shrinking a
// viewport moves its bottom, so a reader who was at the end would silently
// stop being there.
func (m *teaModel) setChatHeight(height int) {
	follow := m.chat.AtBottom()
	m.chat.Height = max(1, height)
	if follow {
		m.chat.GotoBottom()
	}
}

// blockHeight is what the prompt takes: a blank row for air, the status row,
// and the form — the input with a rule above and below it.
func (m *teaModel) blockHeight() int {
	return m.inputHeight + 4 + m.menuRows()
}

// menuLimit is how many command rows are shown at once. The menu used to draw
// every match without being counted in the block, so eleven commands made the
// view taller than the terminal and the rows that fell off the bottom took the
// selection with them: pressing up at the top wrapped to /mouse, which was no
// longer on the screen.
const menuLimit = 8

// menuRows is how tall the command menu is right now — never more than the
// chat can spare, since the block is drawn under it.
func (m *teaModel) menuRows() int {
	found := len(matches(m.input.Value()))
	if found == 0 {
		return 0
	}
	room := m.height - (m.inputHeight + 4) - 2 // two rows of chat, at least
	return max(1, min(found, min(menuLimit, room)))
}

// menuWindow is the slice of the menu that is drawn, and the index the
// selection sits at within it. It follows the selection rather than the top of
// the list, so wrapping from the first command to the last stays visible.
func (m *teaModel) menuWindow(found int) (first, rows int) {
	rows = m.menuRows()
	if m.commandSel >= rows {
		first = min(m.commandSel-rows+1, max(0, found-rows))
	}
	return first, rows
}

// afterTurn keeps the conversation recoverable and announces background work
// that ended while the model was busy. Both belong between turns: a task
// finishing cannot paint the screen itself.
func (m *teaModel) afterTurn() {
	if err := saveSession(m.agent); err != nil && !m.saveFailed {
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

// turnSummary closes a turn with what it cost: how long it took, how much it
// reached for, and what it spent. Said once at the end rather than watched all
// the way through, which is what the status row is for.
func (m *teaModel) turnSummary() string {
	parts := []string{"✻ Done in " + fmtElapsed(time.Since(m.started))}
	if m.toolCalls > 0 {
		tools := "tools"
		if m.toolCalls == 1 {
			tools = "tool"
		}
		parts = append(parts, fmt.Sprintf("%d %s", m.toolCalls, tools))
	}
	if m.usage.Input > 0 || m.usage.Output > 0 {
		parts = append(parts, fmt.Sprintf("↑%s ↓%s tokens", fmtTokens(m.usage.Input), fmtTokens(m.usage.Output)))
		if m.agent.Provider != nil {
			if cost := config.CostUSD(m.agent.Provider.Name(), m.usage.Input, m.usage.Output); cost != "" {
				parts = append(parts, cost)
			}
		}
	}
	return teaDim.Render(strings.Join(parts, " · "))
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
	switch {
	case value == "/exit" || value == "/quit":
		return tea.Quit
	case value == "/clear":
		m.agent.History = nil
		m.lines = nil // not even the echo of /clear survives the clearing
		m.chatted = false
		// A cleared chat is a session starting over, so it opens the way one
		// does: with the welcome box, not with an empty screen.
		m.add(chatEntry{kind: entryBanner, text: providerLabel(m.agent.Provider)})
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
			return tea.EnableMouseCellMotion
		}
		m.addHistory(teaDim.Render("mouse: handed to the terminal — plain drag selects, ^y/^e scroll"))
		return tea.DisableMouse
	case value == "/help":
		m.addHistory(teaTitle.Render("commands") + `
  /connect  connect a provider
  /model    choose a model
  /check    run the project's tests as a task, or /check <command>
  /stop     stop a task: /stop t1
  /compact  summarize the history to free up context
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
                 .ouhai/settings.json "permissions" makes it permanent

` + teaTitle.Render("outside this session") + `
  ouhai -p "..."      one answer and exit — and a pipe works: cat q | ouhai
  ouhai -sessions     the conversations saved so far
  ouhai -resume [id]  carry on with one, on the model it was held with
  AGENTS.md           this project's own instructions, read every prompt —
                      CLAUDE.md and OUHAI.md are read the same way
  .claude/skills/     instructions for particular jobs, opened when needed`)
		return nil
	case strings.HasPrefix(value, "/model"):
		arg := strings.TrimSpace(strings.TrimPrefix(value, "/model"))
		if arg == "" {
			return m.beginModels()
		}
		return m.changeModel(arg)
	case strings.HasPrefix(value, "/connect"):
		return m.beginConnect(strings.TrimSpace(strings.TrimPrefix(value, "/connect")))
	case strings.HasPrefix(value, "/"):
		m.addHistory(teaDim.Render("unknown command: " + value))
		return nil
	case m.agent.Provider == nil:
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
		return teaDoneMsg{err: m.agent.Ask(ctx, value)}
	})
}

func (m *teaModel) changeModel(setting string) tea.Cmd {
	// Whatever happens next, the picker is done: every path below reports
	// through the history, which is only visible on the chat page.
	m.mode = teaPrompt
	m.input.Focus()
	if setting == "" {
		m.addHistory(teaDim.Render("use /model provider/model, for example /model groq/openai/gpt-oss-120b"))
		return nil
	}
	p, err := config.LoadProviderFor(setting)
	if err != nil {
		m.addHistory(teaDim.Render("could not load model: " + err.Error()))
		return nil
	}
	if err := config.SaveModel(setting); err != nil {
		m.addHistory(teaDim.Render("could not save model: " + err.Error()))
		return nil
	}
	m.useProvider(p)
	m.addHistory(teaDim.Render("using " + p.Name()))
	return nil
}

// confirmChoices are the answers, in the order they are offered.
const (
	choiceYes = iota
	choiceAlways
	choiceNo
)

func (m *teaModel) confirmChoices() []string {
	return []string{
		"Yes",
		"Yes, and don't ask again for " + m.confirm.name + " this session",
		"No, and tell ouhai what to do instead",
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

func (m *teaModel) View() string {
	if m.width == 0 {
		return "starting ouhai..."
	}
	m.resizeInput()
	status := m.status
	// The question wins over the spinner: a tool is always asked about while
	// the model is working, so "thinking..." would be the last thing said
	// before the prompt waited forever for an answer nobody knew to give.
	if m.busy && m.confirm == nil {
		status = fmt.Sprintf("%s thinking... %s · esc to interrupt", m.spinner.View(), m.workingHint())
		if m.queued != "" {
			status += " · 1 queued"
		}
	} else if status == "" && !m.wheel {
		status = "✂ the mouse is the terminal's · /mouse to scroll with the wheel"
	}
	// A pending tool call owns the block below the chat: the question, what it
	// is about, and the answers, with nothing else competing for the row. The
	// chat keeps what is left, so the conversation that led here is still
	// readable while it is being decided.
	if m.confirm != nil {
		panel := m.confirmPanel()
		m.setChatHeight(m.height - len(panel))
		return lipgloss.JoinVertical(lipgloss.Left, append([]string{m.chat.View()}, panel...)...)
	}
	if m.mode == teaProviderPicker || m.mode == teaModelPicker {
		return lipgloss.JoinVertical(lipgloss.Left, m.picker.View(), teaDim.Render("enter select · esc cancel"))
	}
	if m.mode == teaKeyEntry {
		masked := strings.Repeat("•", len([]rune(m.keyInput.Value())))
		if masked == "" {
			masked = teaDim.Render(m.keyInput.Placeholder)
		}
		rows := []string{teaTitle.Render("connect " + m.provider)}
		// Where the key comes from, since nobody remembers a console URL and
		// leaving to look it up means losing the prompt.
		if url := config.KeyURL(m.provider); url != "" {
			rows = append(rows, teaDim.Render("get a key: ")+url)
		}
		keep := "esc cancel"
		switch {
		case m.credField != config.FieldKey:
			keep = "esc connects without it"
		case config.APIKey(m.provider) != "":
			keep = "esc keeps the key already saved"
		}
		label := credLabel(m.credField)
		rows = append(rows, teaDim.Render("enter save · "+keep), m.formSurface(label+"  "+masked))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	menu := matches(m.input.Value())
	var commandRows []string
	if len(menu) > 0 {
		first, rows := m.menuWindow(len(menu))
		for i, command := range menu[first:min(first+rows, len(menu))] {
			style := teaDim
			if first+i == m.commandSel {
				style = teaUser
			}
			commandRows = append(commandRows, style.Render(fmt.Sprintf("  %-12s %s", command.name, command.desc)))
		}
	}
	// Flush with the rules under it: an indent here made the whole block one
	// column wider than the terminal. The model hint gets what is left after
	// the status and a space, and is cut to fit — the two used to run into
	// each other when the status was long.
	hint := ""
	if room := m.cols() - lipgloss.Width(status) - 1; room > 3 {
		hint = truncate(m.modelHint(), room)
	}
	statusRow := lipgloss.JoinHorizontal(
		lipgloss.Top,
		teaDim.Render(status),
		lipgloss.PlaceHorizontal(max(1, m.cols()-lipgloss.Width(status)), lipgloss.Right, teaDim.Render(hint)),
	)
	// The chat scrolls inside its own area; the prompt block under it does
	// not move. That is the whole point of taking the screen over: scrolling
	// moves the conversation, not the thing you are typing into.
	// A blank row between the conversation and the block below it. Without
	// it the status runs straight into the last thing said, and the eye has
	// to work out where one ends and the other begins.
	rows := []string{m.chat.View(), ""}
	if len(commandRows) > 0 {
		rows = append(rows, commandRows...)
	}
	rows = append(rows, statusRow, m.formSurface(m.input.View()))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

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
