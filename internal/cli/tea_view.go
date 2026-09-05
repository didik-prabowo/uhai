// What the terminal front end looks like: the whole screen in View, the
// pieces it is made of, and the arithmetic that fits them to the window.
package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

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

func (m *teaModel) rendererText(text string) string {
	rendered, err := m.renderer.Render(text)
	if err != nil {
		return text
	}
	// A code line is one glamour gave a background to: chroma colours the
	// characters, and nothing else in an answer carries a background at all.
	//
	// Making that into a block takes two repairs. Chroma ends every token with
	// a full reset, so the band dies in the gaps between them and never
	// reaches the margin or the padding — each reset is followed by the band
	// being turned on again. And the run of lines is opened and closed with a
	// blank banded line, which is what makes it a rectangle rather than three
	// coloured stripes.
	//
	// m.cols() and not the terminal width: a line as wide as the terminal
	// wraps on its own, which scrolls the screen out from under the renderer.
	band := lipgloss.NewStyle().Background(code)

	// The escape that turns the band on, taken from lipgloss so the colour and
	// the terminal's profile are decided in one place. A terminal with no
	// colour renders the space as itself, and there is no band to draw — the
	// check matters, because trimming a suffix that is not there leaves the
	// space, and a space substituted for every reset would pull the code apart.
	on := ""
	if painted := band.Render(" "); strings.HasSuffix(painted, " \x1b[0m") {
		on = strings.TrimSuffix(painted, " \x1b[0m")
	}
	fill := func(n int) string {
		if n <= 0 {
			return ""
		}
		return band.Render(strings.Repeat(" ", n))
	}

	var rows []string
	inBlock := false
	for _, line := range strings.Split(rendered, "\n") {
		line = strings.TrimRight(line, " \t")

		if on == "" || !strings.Contains(line, "\x1b[48;") {
			if inBlock {
				rows = append(rows, fill(m.cols()))
				inBlock = false
			}
			rows = append(rows, line)
			continue
		}
		if !inBlock {
			rows = append(rows, fill(m.cols()))
			inBlock = true
		}
		// Chroma's own background comes out a shade off ours — it resolves
		// colours through its own profile — so it is stripped and the band is
		// the only one left. It was only ever needed as the mark that says
		// which lines are code.
		width := visibleLen(line)
		line = chromaBackground.ReplaceAllString(line, "")
		line = on + strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+on)
		rows = append(rows, line+fill(m.cols()-width))
	}
	if inBlock {
		rows = append(rows, fill(m.cols()))
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

func (m *teaModel) View() string {
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
	if m.mode == teaProviderPicker || m.mode == teaModelPicker || m.mode == teaSkillPicker {
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

// chromaBackground matches a background escape, whatever depth of colour the
// terminal turned out to support.
var chromaBackground = regexp.MustCompile(`\x1b\[48;[0-9;]*m`)
