// What the terminal front end looks like: the whole screen in View, the
// pieces it is made of, and the arithmetic that fits them to the window.
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
)

func (m *teaModel) render(e chatEntry) string {
	switch e.kind {
	case entryAsk:
		return teaAsk.Width(m.cols()).Render("❯ " + e.text)
	case entryAnswer:
		return m.rendererText(e.text)
	case entryBanner:
		return strings.Join(boxLines(m.cols(),
			accentAt+"✻"+reset+" Welcome to uhai!  "+dim+builtAt()+reset,
			dim+"provider:"+reset+" "+e.text,
			dim+"cwd:"+reset+" "+workingDir(),
			dim+"type / for commands, /exit to quit"+reset,
		), "\n")
	}
	return e.text
}

// newChatViewport is the scrolling chat. Its default keymap steals j, k, f,
// b, u, d and space, which are ordinary typing here, so it keeps only the
// keys the prompt has no use for; the arrows are dealt with in update, where
// it is known whether the prompt itself wants them.
func newChatViewport() viewport.Model {
	v := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	// Shift with the arrows is the only thing that scrolls: it reaches the app
	// whatever the terminal thinks about wheels, and nothing else wants it.
	// The page keys are the prompt's history, like the arrows.
	v.KeyMap = viewport.KeyMap{
		Up:   key.NewBinding(key.WithKeys("shift+up")),
		Down: key.NewBinding(key.WithKeys("shift+down")),
	}
	return v
}

func applyInputStyle(input *textarea.Model, isDark bool) {
	styles := textarea.DefaultStyles(isDark)
	for _, state := range []*textarea.StyleState{&styles.Focused, &styles.Blurred} {
		state.Base = lipgloss.NewStyle()
		state.CursorLine = lipgloss.NewStyle()
		state.EndOfBuffer = lipgloss.NewStyle()
		state.Text = lipgloss.NewStyle().Foreground(text)
		state.Prompt = lipgloss.NewStyle().Foreground(accent)
		state.Placeholder = lipgloss.NewStyle().Foreground(muted)
	}
	input.SetStyles(styles)
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
			if cost := config.CostUSD(m.agent.Provider.Name(), provider.Usage{Input: m.usage.Input, Output: out, CacheRead: m.usage.CacheRead, CacheWrite: m.usage.CacheWrite}); cost != "" {
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
	var rows []string
	// The working out sits above the answer and is dimmed: it is worth
	// watching while it is all there is, and worth nothing once the answer
	// starts.
	if m.thinking != "" {
		for _, line := range strings.Split(m.thinking, "\n") {
			for _, row := range wrapHanging(line, m.cols()) {
				rows = append(rows, teaDim.Render(row))
			}
		}
	}
	if m.stream == "" {
		return rows
	}
	for _, line := range strings.Split(m.stream, "\n") {
		rows = append(rows, wrapHanging(line, m.cols())...)
	}
	return rows
}

// collapseThinking replaces the working out with a note that it happened.
// Keeping all of it would bury the answer — a thinking model writes more of it
// than of the reply — and dropping it silently would leave the twenty seconds
// unexplained.
func (m *teaModel) collapseThinking() {
	if m.thinking == "" {
		return
	}
	took := time.Since(m.thinkStart).Round(time.Second)
	m.thinking = ""
	m.addHistory(teaDim.Render(fmt.Sprintf("✻ thought for %s", took)))
}

// rendererText renders an answer: the prose through glamour, and each fenced
// code block drawn here, because a block is a shape and glamour draws it as
// flowing text.
func (m *teaModel) rendererText(text string) string {
	var out []string
	for _, part := range splitFences(text) {
		if part.code {
			// A blank line each side. Without them a block sits against the
			// sentence that introduced it, and two blocks with one line of
			// prose between them read as one block with a caption in it.
			out = append(out, "", codeBlock(part.text, part.lang, m.cols()), "")
			continue
		}
		if prose := m.prose(part.text); prose != "" {
			out = append(out, prose)
		}
	}
	// The blank a block asks for at its edge is not wanted at the edge of the
	// answer, where the chat already puts one.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func (m *teaModel) prose(text string) string {
	rendered, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
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
	m.chat.SetWidth(m.cols())
	m.setChatHeight(m.height - m.blockHeight())
}

// setChatHeight resizes the chat and keeps it where it was: shrinking a
// viewport moves its bottom, so a reader who was at the end would silently
// stop being there.
func (m *teaModel) setChatHeight(height int) {
	follow := m.chat.AtBottom()
	m.chat.SetHeight(max(1, height))
	if follow {
		m.chat.GotoBottom()
	}
}

// blockHeight is what the prompt takes: a blank row for air, the status row,
// and the form — the input with a rule above and below it.
func (m *teaModel) blockHeight() int {
	return m.inputHeight + 4 + m.menuRows()
}

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
			if cost := config.CostUSD(m.agent.Provider.Name(), m.usage); cost != "" {
				parts = append(parts, cost)
			}
		}
	}
	return teaDim.Render(strings.Join(parts, " · "))
}

// View returns a tea.View rather than a string: bubbletea v2 lets a view
// carry layers and a cursor, and the interface asks for the richer type even
// when, as here, the answer is still one screen of text.
// View is the screen plus the two things bubbletea v2 moved onto it. The alt
// screen and the mouse used to be program options and commands; they are
// properties of the view now, declared every frame, which suits /mouse exactly
// — the toggle sets a bool and the next frame carries it, with no command to
// send and no state kept anywhere else.
func (m *teaModel) View() tea.View {
	v := tea.NewView(m.screen())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeNone
	if m.wheel {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *teaModel) screen() string {
	if m.width == 0 {
		return "starting uhai..."
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
	if m.pickerOpen() {
		return lipgloss.JoinVertical(lipgloss.Left, m.picker.View(), teaDim.Render("enter select · / search · esc cancel"))
	}
	if m.mode == teaCustomForm {
		rows := []string{
			teaTitle.Render("add an endpoint"),
			teaDim.Render("tab moves · enter saves all three · esc cancel"),
		}
		for i, label := range customLabels {
			value := m.custom.values[i]
			if i == m.custom.focused {
				value = m.keyInput.Value()
			}
			shown := value
			if i == customKey {
				shown = strings.Repeat("•", len([]rune(value)))
			}
			if shown == "" {
				shown = teaDim.Render(customHints[i])
			}
			// The focused row is the one with a cursor on it; the others are
			// what has been typed so far, which is the whole point of showing
			// three at once.
			caret := "  "
			if i == m.custom.focused {
				caret = teaUser.Render("> ")
			}
			rows = append(rows, m.formSurface(caret+teaDim.Render(label+"  ")+shown))
		}
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
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

// pickerOpen reports whether one of the lists is over the chat. It is asked in
// three places — what to draw, where a key goes, and where everything that is
// not a key goes — and the third is the one that matters: bubbletea's filter
// answers asynchronously, so a picker that does not receive its own messages
// accepts a search and never applies one.
func (m *teaModel) pickerOpen() bool {
	switch m.mode {
	case teaProviderPicker, teaModelPicker, teaSkillPicker, teaDisconnectPicker:
		return true
	}
	return false
}
