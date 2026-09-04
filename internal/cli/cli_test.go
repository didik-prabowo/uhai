package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/didik-prabowo/ouhai/internal/agent"
	"github.com/didik-prabowo/ouhai/internal/config"
	"github.com/didik-prabowo/ouhai/internal/provider"
	"github.com/didik-prabowo/ouhai/internal/provider/anthropic"
	"github.com/didik-prabowo/ouhai/internal/task"
)

// waitTries is how long a test waits for a goroutine to get somewhere, at 5ms
// a step: five seconds. The answer arrives in milliseconds, so the budget only
// matters on a machine busy with something else — where a tight one fails and
// teaches nobody anything.
const waitTries = 1000

func TestMatches(t *testing.T) {
	if got := matches("halo"); got != nil {
		t.Fatalf("plain text must not open the menu: %v", got)
	}
	if got := matches("/"); len(got) != len(commands) {
		t.Fatalf("\"/\" must list every command, got %d", len(got))
	}
	if got := matches("/e"); len(got) != 1 || got[0].name != "/exit" {
		t.Fatalf("\"/e\" should match only /exit, got %v", got)
	}
	if got := matches("/zz"); got != nil {
		t.Fatalf("a non-matching prefix must yield nothing, got %v", got)
	}
}

func TestVisibleLenAbaikanWarna(t *testing.T) {
	if got := visibleLen(dim + "halo" + reset); got != 4 {
		t.Fatalf("colour escapes must not count as columns, got %d", got)
	}
}

func TestWrapLine(t *testing.T) {
	got := wrapLine("abcdefghij", 4)
	want := []string{"abcd", "efgh", "ij"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows: %q", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}

	// Coloured lines are split by printed columns, not byte length.
	colored := wrapLine(accentAt+"abcdef"+reset, 4)
	if len(colored) != 2 || visibleLen(colored[0]) != 4 {
		t.Fatalf("coloured line split wrongly: %q", colored)
	}
}

func TestWorkingHint(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.started = time.Now().Add(-90 * time.Second)
	if got := m.workingHint(); got != "(1m 30s)" {
		t.Fatalf("without usage: got %q", got)
	}
	m.started = time.Now().Add(-5 * time.Second)
	m.usage = provider.Usage{Input: 1200, Output: 340}
	if got := m.workingHint(); got != "(5s · ↑1.2k ↓340 tokens)" {
		t.Fatalf("with usage: got %q", got)
	}
	m.streamed = 400 // still streaming: ~100 more tokens, estimated
	if got := m.workingHint(); got != "(5s · ↑1.2k ↓440 tokens)" {
		t.Fatalf("while streaming: got %q", got)
	}

	// With a model whose price does not depend on its host, the turn is
	// priced as it runs.
	p, err := anthropic.New(anthropic.Options{Label: "anthropic", BaseURL: "https://example.invalid/v1", Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	m.useProvider(p)
	if got := m.workingHint(); !strings.HasSuffix(got, "tokens · $0.01)") {
		t.Fatalf("a priced model must price the turn: %q", got)
	}
}

func TestPickerAlwaysReturnsToChat(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)

	m.mode = teaModelPicker
	m.changeModel("nope/not-a-model") // even a model that cannot load
	if m.mode != teaPrompt {
		t.Fatalf("after choosing a model the picker must close, mode = %v", m.mode)
	}

	m.mode = teaProviderPicker
	m.updatePicker(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != teaPrompt {
		t.Fatalf("esc must cancel the picker, mode = %v", m.mode)
	}
}

func TestAnswerStreamsThenSettles(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	printed := len(m.lines) // the welcome banner

	m.Update(teaDeltaMsg("hal"))
	m.Update(teaDeltaMsg("o dunia"))
	if !strings.Contains(m.View(), "halo dunia") {
		t.Fatalf("the answer must appear while it streams:\n%s", m.View())
	}
	if m.streamed != len("halo dunia") {
		t.Fatalf("streamed chars = %d", m.streamed)
	}

	// Once complete the answer leaves the live block and is printed into the
	// terminal's scrollback instead — here, with no program running, that is
	// the pending history.
	m.Update(teaTextMsg("halo dunia"))
	if m.stream != "" || len(m.lines) != printed+1 {
		t.Fatalf("finished answer: stream = %q, history = %q", m.stream, m.lines)
	}
	if strings.Contains(m.View(), "halo dunia") {
		t.Fatalf("the finished answer must not stay in the live block:\n%s", m.View())
	}
}

func TestTypingWhileBusyIsQueued(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.busy = true

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("halo")})
	if m.input.Value() != "halo" {
		t.Fatalf("the prompt must accept typing while the model works, got %q", m.input.Value())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.queued != "halo" || m.input.Value() != "" {
		t.Fatalf("enter must queue, queued = %q input = %q", m.queued, m.input.Value())
	}

	before := len(m.lines)
	m.Update(teaDoneMsg{})
	if m.queued != "" || len(m.lines) <= before {
		t.Fatalf("the queued prompt must be sent when the turn ends, queued = %q", m.queued)
	}
}

func TestEscInterruptsTheTurn(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	stopped := false
	m.busy, m.cancel = true, func() { stopped = true }
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !stopped {
		t.Fatal("esc must stop the model while it is working")
	}

	// What was typed meanwhile is handed back, not fired at the model the
	// user just stopped.
	m.queued = "halo"
	m.Update(teaDoneMsg{err: fmt.Errorf("provider error: %w", context.Canceled)})
	if m.busy || m.queued != "" || m.input.Value() != "halo" {
		t.Fatalf("after an interrupt: busy=%v queued=%q input=%q", m.busy, m.queued, m.input.Value())
	}
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "interrupted") {
		t.Fatalf("an interrupt must say so, got %q", last)
	}
}

func TestQuestionIsBanded(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.lines = nil

	m.input.SetValue("halo")
	m.submit()

	asked := m.render(m.lines[0])
	if m.lines[0].kind != entryAsk {
		t.Fatalf("a question must be kept as one, got kind %v", m.lines[0].kind)
	}
	// The band spans the chat, so the question stands apart from the answer.
	if visibleLen(asked) != m.cols() {
		t.Fatalf("the band must span the width, got %d columns: %q", visibleLen(asked), asked)
	}
	// The colours themselves are stripped in a test (no terminal to write to),
	// so the band is checked on the style.
	if teaAsk.GetBackground() == (lipgloss.NoColor{}) {
		t.Fatal("the question must read back on a background")
	}
}

func TestAnswerHasNoPaddedEdges(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	out := m.rendererText("Halo, ini jawaban pendek.\n\n- satu\n- dua\n")
	if strings.HasPrefix(out, "\n") || strings.HasSuffix(out, "\n") {
		t.Fatalf("an answer must not carry blank lines of its own: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Fatalf("glamour's padding must be trimmed: %q", line)
		}
	}
}

// The block is only as tall as it needs to be: no padding down to the bottom
// edge, since that padding was terminal content that moved with the chat
// whenever the screen was scrolled — taking the prompt with it.
// The prompt is pinned: the block fills the screen exactly, the chat takes
// whatever the form leaves, and scrolling moves the chat alone.
func TestPromptIsPinnedAndChatScrolls(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	rows := strings.Split(m.View(), "\n")
	if len(rows) != 24 {
		t.Fatalf("the screen must be filled exactly, got %d rows", len(rows))
	}
	if !strings.Contains(rows[23], "─") {
		t.Fatalf("the form must sit on the last row, got %q", rows[23])
	}

	for i := 0; i < waitTries; i++ {
		m.addHistory(fmt.Sprintf("line %d", i))
	}
	bottom := m.chat.YOffset

	// Shift with the arrows scrolls; the arrows alone are the prompt's
	// history, so the wheel can never type into the box.
	m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	if m.chat.YOffset >= bottom {
		t.Fatalf("shift+up must scroll the chat, offset stayed at %d", m.chat.YOffset)
	}
	scrolled := m.chat.YOffset
	// The page keys are history, not a second way to scroll: on a Mac they
	// are fn with the arrows, which the hand reads as the same key.
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.chat.YOffset != scrolled {
		t.Fatalf("pgup must not scroll the chat, offset moved to %d", m.chat.YOffset)
	}
	if got := strings.Split(m.View(), "\n"); !strings.Contains(got[23], "─") {
		t.Fatalf("scrolling must not move the form, last row is %q", got[23])
	}

	// Typing is not scrolling, and it does not follow the chat back down.
	for _, key := range []string{"j", "k", "f", "b", "u", "d", " "} {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
	if m.chat.YOffset != scrolled {
		t.Fatalf("typing must not scroll the chat, offset moved %d → %d", scrolled, m.chat.YOffset)
	}
}

// Entries are kept as they were written, so a resize re-renders them at the
// new width instead of leaving a chat laid out for the old one.
func TestResizeRelaysTheChat(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.input.SetValue("halo")
	m.submit()
	// Found by kind rather than by counting: submit adds blank lines and a
	// note of its own, and an index would go stale the moment either changes.
	ask := -1
	for i, e := range m.lines {
		if e.kind == entryAsk {
			ask = i
		}
	}
	if ask < 0 {
		t.Fatal("the question must be in the chat")
	}
	wide := m.render(m.lines[ask])

	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	narrow := m.render(m.lines[ask])
	if visibleLen(wide) != 80 || visibleLen(narrow) != 60 {
		t.Fatalf("the chat must follow the width: %d then %d", visibleLen(wide), visibleLen(narrow))
	}
	if m.chat.Height != 20-m.blockHeight() {
		t.Fatalf("the chat must take what the prompt leaves, got %d", m.chat.Height)
	}
}

func TestPromptHistoryWalksBackFive(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	for _, prompt := range []string{"satu", "dua", "tiga", "empat", "lima", "enam", "enam"} {
		m.input.SetValue(prompt)
		m.submit()
	}
	// Only the last five are kept, and a repeat does not take a slot.
	if want := []string{"dua", "tiga", "empat", "lima", "enam"}; !slices.Equal(m.past, want) {
		t.Fatalf("history = %q, want %q", m.past, want)
	}

	m.input.SetValue("draf") // what is being typed survives the walk
	for i, want := range []string{"enam", "lima", "empat", "tiga", "dua", "dua"} {
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		if m.input.Value() != want {
			t.Fatalf("pgup %d gave %q, want %q", i+1, m.input.Value(), want)
		}
	}
	for i, want := range []string{"tiga", "empat", "lima", "enam", "draf", "draf"} {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		if m.input.Value() != want {
			t.Fatalf("down %d gave %q, want %q", i+1, m.input.Value(), want)
		}
	}
}

// Ctrl+T is the way out of the bargain the wheel costs: captured, the mouse
// scrolls the chat; released, the terminal can select text again.
// The same swap without a key: Ctrl+T is a tmux prefix in some setups, and a
// key that never reaches the app is no way out of the bargain.
// Control characters no terminal or multiplexer gets to translate: the chat
// has to be scrollable even where shift with the arrows never arrives.
func TestControlKeysScroll(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < waitTries; i++ {
		m.addHistory(fmt.Sprintf("line %d", i))
	}

	bottom := m.chat.YOffset
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	up := m.chat.YOffset
	if up >= bottom {
		t.Fatalf("ctrl+y must scroll up, offset stayed at %d", up)
	}
	if m.input.Value() != "" {
		t.Fatalf("scrolling must leave the prompt alone, got %q", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.chat.YOffset <= up {
		t.Fatalf("ctrl+e must scroll back down, offset stayed at %d", m.chat.YOffset)
	}
}

// The wheel scrolls the chat by default; /mouse hands the mouse back to the
// terminal for the cases where holding shift is not enough.
func TestMouseCommandLendsTheWheel(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < waitTries; i++ {
		m.addHistory(fmt.Sprintf("line %d", i))
	}

	if !m.wheel {
		t.Fatal("the wheel scrolls the chat out of the box")
	}
	bottom := m.chat.YOffset
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.chat.YOffset >= bottom {
		t.Fatalf("the wheel must scroll the chat, offset stayed at %d", m.chat.YOffset)
	}
	// /mouse hands the mouse over whole, for a terminal whose shift-drag is
	// not enough, and says so while it is gone.
	m.input.SetValue("/mouse")
	if cmd := m.submit(); cmd == nil || m.wheel {
		t.Fatal("/mouse must hand the mouse back")
	}
	if !strings.Contains(m.View(), "the terminal's") {
		t.Fatalf("the status row must say the mouse is gone:\n%s", m.View())
	}
}

func TestClearStartsOverWithTheWelcome(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.input.SetValue("halo")
	m.submit()

	m.input.SetValue("/clear")
	m.submit()
	if len(m.lines) != 1 || m.lines[0].kind != entryBanner {
		t.Fatalf("a cleared chat must open with the welcome box, got %+v", m.lines)
	}
	if m.chatted {
		t.Fatal("the first question after clearing must not open with a blank line")
	}
}

// Everything the old renderer could do, the new one must: the commands are
// the same, and a turn leaves the session on disk behind it.
func TestPortedCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Without a provider these say so rather than pretending to work.
	for _, cmd := range []string{"/compact", "/bg do something"} {
		m.lines = nil
		m.input.SetValue(cmd)
		m.submit()
		if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "no provider") {
			t.Fatalf("%s without a provider said %q", cmd, last)
		}
	}

	m.lines = nil
	m.input.SetValue("/tasks")
	m.submit()
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "no tasks yet") {
		t.Fatalf("/tasks said %q", last)
	}

	// A finished turn writes the conversation out, so -resume has something
	// to pick up.
	m.agent.History = []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}},
	}}
	m.Update(teaDoneMsg{})
	saved, err := config.LatestSession()
	if err != nil {
		t.Fatalf("the session must be saved after a turn: %v", err)
	}
	if len(saved.Messages) != 1 {
		t.Fatalf("saved session holds %d messages", len(saved.Messages))
	}
}

// The window belongs to the model, so switching model has to move it — a
// 200k model compacting at 32k would throw away history for nothing.
func TestModelChangeMovesTheContextWindow(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.agent.MaxContextTokens = 32_000

	p, err := anthropic.New(anthropic.Options{
		Label: "anthropic", BaseURL: "https://example.invalid/v1", Model: "claude-sonnet-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.useProvider(p)

	if m.agent.Provider != provider.Provider(p) {
		t.Fatal("the provider must be the one handed over")
	}
	if m.agent.MaxContextTokens != config.ContextWindow("anthropic/claude-sonnet-5") {
		t.Fatalf("the window did not follow the model, got %d", m.agent.MaxContextTokens)
	}
}

// The other kind of task: a command, with no model anywhere in it. It has to
// work with no provider connected, since nothing about it needs one.
func TestCheckRunsWithoutAProvider(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.input.SetValue("/check echo halo && echo dunia")
	m.submit()
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "running echo halo") {
		t.Fatalf("/check said %q", last)
	}

	var done task.Task
	for i := 0; i < waitTries && done.ID == ""; i++ {
		for _, got := range m.agent.Tasks.Snapshot() {
			if got.Status == task.StatusDone {
				done = got
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if done.ID == "" {
		t.Fatal("the check never finished")
	}
	if done.Report != "halo\ndunia" {
		t.Fatalf("the command's output is the report, got %q", done.Report)
	}

	// A command that fails is a failed task, not a silent one.
	m.input.SetValue("/check exit 1")
	m.submit()
	for i := 0; i < waitTries; i++ {
		for _, got := range m.agent.Tasks.Snapshot() {
			if got.Status == task.StatusFailed {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a failing command must be recorded as a failed task")
}

func TestCheckKeepsTheEndOfLongOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := lastLines(strings.Join(lines, "\n"), 3)
	if got != "…\nline 97\nline 98\nline 99" {
		t.Fatalf("the tail is what matters, got %q", got)
	}
}

// A check can be looked in on while it runs, and stopped without waiting for
// whatever it was doing to finish on its own.
func TestCheckCanBeWatchedAndStopped(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.input.SetValue("/check echo mulai; sleep 30")
	m.submit()

	// The first line arrives long before the command is over.
	var report string
	for i := 0; i < waitTries; i++ {
		report = tasksReport(m.agent, "t1")
		if strings.Contains(report, "mulai") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(report, "is still running") || !strings.Contains(report, "mulai") {
		t.Fatalf("a running check must show what it has printed: %q", report)
	}

	m.input.SetValue("/stop t1")
	m.submit()
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "stopping t1") {
		t.Fatalf("/stop said %q", last)
	}

	for i := 0; i < waitTries; i++ {
		if strings.Contains(tasksReport(m.agent, ""), "failed") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a stopped check must end rather than run its full 30 seconds")
}

// A line longer than the terminal is drawn as two rows. The chat has to know
// that: counting it as one slides the newest line under the prompt, where it
// stays for the rest of the session.
func TestLongLinesAreWrappedInTheChat(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.lines = nil

	m.addHistory(strings.Repeat("x", 100)) // three rows at this width
	if got := m.chat.TotalLineCount(); got != 3 {
		t.Fatalf("a 100-column line at width %d is %d rows, the chat counted %d", m.cols(), 3, got)
	}

	// Fill past the viewport and check the newest line is the one on screen.
	for i := 0; i < 20; i++ {
		m.addHistory(fmt.Sprintf("baris %d", i))
	}
	rows := strings.Split(m.chat.View(), "\n")
	if last := strings.TrimSpace(rows[len(rows)-1]); last != "baris 19" {
		t.Fatalf("the newest line must be the visible one, got %q", last)
	}
}

// A tool call has to be readable before it is approved: a path and the lines
// that change, not the JSON the model happened to send.
func TestConfirmShowsWhatWillHappen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {\n\tprintln(\"halo\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := confirmDetail("edit_file", fmt.Sprintf(`{"path":%q,"old":"\tprintln(\"halo\")","new":"\tprintln(\"dunia\")"}`, path))
	// lipgloss turns the tabs into spaces on the way out, so the markers and
	// the text are what to look for.
	if !strings.Contains(edit, `- `) || !strings.Contains(edit, `println("halo")`) {
		t.Fatalf("an edit must show what goes:\n%s", edit)
	}
	if !strings.Contains(edit, `+ `) || !strings.Contains(edit, `println("dunia")`) {
		t.Fatalf("an edit must show what comes:\n%s", edit)
	}

	// Rewriting an existing file is a diff against what is there, and the
	// title says which kind of change it is.
	rewrite := confirmDetail("write_file", fmt.Sprintf(`{"path":%q,"content":"package main\n"}`, path))
	if !strings.Contains(rewrite, "- }") {
		t.Fatalf("a rewrite must show what goes:\n%s", rewrite)
	}
	if got := confirmTitle("write_file", fmt.Sprintf(`{"path":%q}`, path)); got != "Rewrite "+path {
		t.Fatalf("title = %q", got)
	}

	// A new file has nothing to compare against, so all of it is added.
	fresh := confirmDetail("write_file", `{"path":"/nowhere/new.go","content":"package main\n"}`)
	if strings.Contains(fresh, "- ") {
		t.Fatalf("a new file has nothing to remove:\n%s", fresh)
	}
	if got := confirmTitle("write_file", `{"path":"/nowhere/new.go"}`); got != "Create /nowhere/new.go" {
		t.Fatalf("title = %q", got)
	}

	if got := confirmDetail("run_bash", `{"command":"go test ./..."}`); !strings.Contains(got, "$ go test ./...") {
		t.Fatalf("a command must be shown as one: %q", got)
	}
}

// "always" used to approve one call and ask again on the next.
func TestAlwaysIsRemembered(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	reply := make(chan bool, 1)
	m.Update(teaConfirmMsg{request: teaConfirm{name: "run_bash", input: `{"command":"ls"}`, reply: reply}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !<-reply {
		t.Fatal("always must approve the call it answers")
	}
	if m.decide("run_bash", `{"command":"anything else"}`) != config.PermAllow {
		t.Fatal("always must approve the next one too")
	}
	if m.decide("write_file", `{"path":"x"}`) != config.PermAsk {
		t.Fatal("allowing one tool must not allow another")
	}
}

// The question is asked while the model works, so it has to outrank the
// spinner: "thinking…" was the last thing said before the prompt waited
// forever for an answer nobody knew to give.
func TestConfirmQuestionOutranksTheSpinner(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.busy = true

	m.Update(teaConfirmMsg{request: teaConfirm{name: "run_bash", input: `{"command":"ls"}`, reply: make(chan bool, 1)}})
	view := m.View()
	for _, want := range []string{"Shell command", "$ ls", "Do you want to proceed?", "1. Yes", "3. No"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the question must show %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "thinking") {
		t.Fatalf("the spinner must give way to it:\n%s", view)
	}
}

// A background task's result has to reach the model, or the loop is: it fails,
// you read it, you retype it. The report travels with the next prompt rather
// than as a message of its own, since the Messages API wants roles to
// alternate and two user messages in a row would be refused.
func TestFinishedTaskTravelsWithTheNextPrompt(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.input.SetValue("/check echo gagal total; exit 2")
	m.submit()
	for i := 0; i < waitTries && len(m.notes) == 0; i++ {
		m.collectFinished()
		time.Sleep(5 * time.Millisecond)
	}
	if len(m.notes) != 1 || !strings.Contains(m.notes[0], "gagal total") || !strings.Contains(m.notes[0], "failed") {
		t.Fatalf("the note must carry how it went and what it said: %q", m.notes)
	}

	// It is folded into the prompt, and said out loud so it is not a surprise.
	m.agent.Provider = stubProvider{}
	m.input.SetValue("kenapa gagal?")
	m.submit()
	if len(m.notes) != 0 {
		t.Fatal("a report travels once")
	}
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "background report") {
		t.Fatalf("sending a report must be visible, got %q", last)
	}
}

// stubProvider answers nothing, which is all this test needs: what matters is
// the prompt that reaches it.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub/stub" }
func (stubProvider) Send(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return &provider.Response{
		Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "ok"}},
		StopReason: provider.StopEndTurn,
	}, nil
}

// A background task runs the same model as the session, so it has to inherit
// what is known about that model — not the defaults for a model nobody named.
func TestBackgroundTaskInheritsTheModelsLimits(t *testing.T) {
	a := agent.New(stubProvider{})
	a.MaxContextTokens, a.UseTools = 200_000, false

	if err := spawnBackground(a, "sebutkan 2 warna"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < waitTries; i++ {
		if list := a.Tasks.Snapshot(); len(list) == 1 && ended(list[0]) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Tokens are the task's own estimate of its history; what matters here is
	// that it ran at all with the session's settings rather than crashing on
	// a nil provider or compacting at the default.
	if list := a.Tasks.Snapshot(); len(list) != 1 || list[0].Status != task.StatusDone {
		t.Fatalf("the task did not finish: %+v", list)
	}
}

// A key that is refused is the one you most need to replace, and /connect
// used to skip straight past it whenever anything was saved.
func TestConnectOffersToReplaceASavedKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, env := range []string{"GROQ_API_KEY", "OUHAI_API_KEY", "OUHAI_MODEL"} {
		t.Setenv(env, "")
	}
	if err := config.Save("groq", config.Creds{config.FieldKey: "kunci-lama-yang-salah"}); err != nil {
		t.Fatal(err)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("groq")
	if m.mode != teaKeyEntry {
		t.Fatal("a provider with a saved key must still offer to take a new one")
	}
	if !strings.Contains(m.keyInput.Placeholder, "keep the one saved") {
		t.Fatalf("the prompt must say what escape does, got %q", m.keyInput.Placeholder)
	}

	// Escaping keeps what is stored rather than throwing the connection away.
	m.updatePicker(tea.KeyMsg{Type: tea.KeyEsc})
	if got := config.APIKey("groq"); got != "kunci-lama-yang-salah" {
		t.Fatalf("escape must keep the saved key, got %q", got)
	}

	// Typing one replaces it.
	m.selectProvider("groq")
	m.keyInput.SetValue("kunci-baru")
	m.updatePicker(tea.KeyMsg{Type: tea.KeyEnter})
	if got := config.APIKey("groq"); got != "kunci-baru" {
		t.Fatalf("a new key must replace the old one, got %q", got)
	}
}

// The environment beats the file, so saving a key while one is exported looks
// like nothing happened unless it is said.
func TestConnectWarnsWhenTheEnvironmentWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "dari-env")

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("groq")
	m.keyInput.SetValue("dari-ketikan")
	m.updatePicker(tea.KeyMsg{Type: tea.KeyEnter})

	var said bool
	for _, e := range m.lines {
		said = said || strings.Contains(e.text, "GROQ_API_KEY is set")
	}
	if !said {
		t.Fatalf("the environment winning must be said out loud: %+v", m.lines)
	}
}

// Nobody remembers a console URL, and leaving to look it up loses the prompt.
func TestKeyEntryShowsWhereToGetOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"ANTHROPIC_API_KEY", "OUHAI_API_KEY", "OUHAI_MODEL"} {
		t.Setenv(env, "")
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("anthropic")

	view := m.View()
	if !strings.Contains(view, config.KeyURL("anthropic")) {
		t.Fatalf("the key page must be on screen:\n%s", view)
	}
	if !strings.Contains(view, "esc cancel") {
		t.Fatalf("with no key saved, escape backs out:\n%s", view)
	}

	// With one saved, escape means something else, and says so.
	if err := config.Save("anthropic", config.Creds{config.FieldKey: "k"}); err != nil {
		t.Fatal(err)
	}
	m.selectProvider("anthropic")
	if !strings.Contains(m.View(), "esc keeps the key already saved") {
		t.Fatalf("escape must say what it keeps:\n%s", m.View())
	}
}

// Patterns decide per command, which is the only useful shape for a shell:
// "git *" allowed and "git push *" denied is a rule a person actually wants.
func TestCommandPatternsDecideBeforeAnyoneIsAsked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ouhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"allow":["Bash(git:*)"],"deny":["Bash(git push:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".ouhai", "settings.json"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	for command, want := range map[string]string{
		"git status --short":     config.PermAllow,
		"git push origin main":   config.PermDeny, // the longer rule wins
		"rm -rf /":               config.PermAsk,  // nothing said, so the default
		"gh pr create --title x": config.PermAsk,
	} {
		got := m.decide("run_bash", fmt.Sprintf(`{"command":%q}`, command))
		if got != want {
			t.Errorf("%q → %q, want %q", command, got, want)
		}
	}

	// A session's "always" cannot overrule what the project denied.
	m.allowed.add("run_bash")
	if got := m.decide("run_bash", `{"command":"git push origin main"}`); got != config.PermDeny {
		t.Fatalf("a denied command must stay denied, got %q", got)
	}
}

// The question is answered by moving to an answer, not by remembering a
// letter: a prompt nobody reads is decoration, and the letters were the part
// people stopped reading.
func TestConfirmPanelIsNavigable(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	ask := func() chan bool {
		reply := make(chan bool, 1)
		m.Update(teaConfirmMsg{request: teaConfirm{name: "run_bash", input: `{"command":"ls"}`, reply: reply}})
		return reply
	}

	// Down moves the cursor, enter takes what is under it.
	reply := ask()
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(m.View(), "❯ 3. No") {
		t.Fatalf("the cursor must move:\n%s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if <-reply {
		t.Fatal("the third answer is no")
	}

	// A number answers straight away, and 2 remembers the tool.
	reply = ask()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if !<-reply {
		t.Fatal("the second answer is yes")
	}
	if !m.allowed.has("run_bash") {
		t.Fatal("and it is the one that remembers")
	}

	// The letters still work, for hands that already know them.
	reply = ask()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if <-reply {
		t.Fatal("n is still no")
	}

	// Escape is no, not a way to leave the question unanswered — the tool call
	// is waiting on the other end of that channel.
	reply = ask()
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if <-reply {
		t.Fatal("esc is no")
	}
	if m.confirm != nil {
		t.Fatal("and the question is over")
	}
}

// The panel takes the room it needs from the chat, not from the screen: the
// conversation that led to the question has to stay readable while it is being
// answered.
func TestConfirmPanelLeavesTheChatVisible(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < 30; i++ {
		m.addHistory(fmt.Sprintf("baris %d", i))
	}

	m.Update(teaConfirmMsg{request: teaConfirm{name: "run_bash", input: `{"command":"ls"}`, reply: make(chan bool, 1)}})

	rows := strings.Split(m.View(), "\n")
	if len(rows) != 24 {
		t.Fatalf("the view must still be one screen, got %d rows", len(rows))
	}
	if !strings.Contains(m.View(), "baris 29") {
		t.Fatalf("the newest chat line must survive the panel:\n%s", m.View())
	}
	if !strings.Contains(rows[len(rows)-1], "╰") {
		t.Fatalf("and the panel must end the block, last row is %q", rows[len(rows)-1])
	}
}

// A tool call is a line of the conversation, so it has to read like one.
func TestToolCallsReadAsWhatTheyDo(t *testing.T) {
	for _, c := range []struct{ name, input, want string }{
		{"run_bash", `{"command":"go run hello-world/main.go"}`, "run_bash go run hello-world/main.go"},
		{"edit_file", `{"path":"hello-world/main.go","old":"a","new":"b"}`, "edit_file hello-world/main.go"},
		{"read_file", `{"path":"internal/cli/tea.go"}`, "read_file internal/cli/tea.go"},
		{"grep", `{"pattern":"func Test","include":"*.go","path":"internal"}`, "grep func Test in *.go under internal"},
		{"glob", `{"pattern":"**/*_test.go"}`, "glob **/*_test.go"},
		{"read_file", `not json at all`, "read_file not json at all"},
	} {
		if got := toolLine(c.name, c.input); got != c.want {
			t.Errorf("%s → %q, want %q", c.name, got, c.want)
		}
	}
}

// The block leaves a blank row between itself and the conversation: without
// one the status runs straight into the last thing said.
func TestBlockKeepsItsDistanceFromTheChat(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < 40; i++ {
		m.addHistory(fmt.Sprintf("baris %d", i))
	}

	rows := strings.Split(m.View(), "\n")
	if len(rows) != 24 {
		t.Fatalf("the view is one screen, got %d rows", len(rows))
	}
	// chat … | blank | status | rule | input | rule
	if blank := rows[len(rows)-5]; strings.TrimSpace(blank) != "" {
		t.Fatalf("the row above the status must be blank, got %q", blank)
	}
	if !strings.Contains(rows[len(rows)-6], "baris 39") {
		t.Fatalf("and the chat must end just above it, got %q", rows[len(rows)-6])
	}
}

// A turn closes with what it cost, said once, rather than leaving the reader
// to remember what the status row was showing before it cleared.
func TestTurnClosesWithASummary(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.started = time.Now().Add(-111 * time.Second)
	m.toolCalls, m.usage = 3, provider.Usage{Input: 2500, Output: 340}

	m.Update(teaDoneMsg{})
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "✻ Done in 1m 51s · 3 tools · ↑2.5k ↓340 tokens") {
		t.Fatalf("summary = %q", last)
	}
	// It stands apart from the answer: it is about the turn, not part of it.
	if blank := m.lines[len(m.lines)-2].text; strings.TrimSpace(blank) != "" {
		t.Fatalf("the summary needs a line above it, got %q", blank)
	}

	// A turn that was stopped, or that failed, says that instead — there is
	// nothing to be pleased about.
	for _, err := range []error{context.Canceled, fmt.Errorf("provider error")} {
		m.lines = nil
		m.Update(teaDoneMsg{err: err})
		for _, line := range m.lines {
			if strings.Contains(line.text, "Done in") {
				t.Fatalf("a failed turn must not report success: %q", line.text)
			}
		}
		// However it ended, the block stands clear of what was said before it.
		if blank := m.lines[0].text; strings.TrimSpace(blank) != "" {
			t.Fatalf("the closing block needs a line above it, got %q", blank)
		}
	}

	// A turn that failed says what to do next: stopping there with only the
	// reason leaves it unclear whether the reading it did was lost with it.
	m.lines = nil
	m.Update(teaDoneMsg{err: fmt.Errorf("provider error (HTTP 429)")})
	if last := m.lines[len(m.lines)-1].text; !strings.Contains(last, "still in the history") {
		t.Fatalf("a failed turn should say the work survived it: %q", last)
	}

	// The next turn counts from zero.
	m.agent.Provider = stubProvider{}
	m.input.SetValue("halo")
	m.submit()
	if m.toolCalls != 0 {
		t.Fatalf("tools carried over: %d", m.toolCalls)
	}
}

// The palette lives in one file. A colour written straight into a view is how
// a scheme drifts: one place gets the new accent and three keep the old one.
func TestColoursComeFromTheTheme(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		if file == "theme.go" || file == "text.go" || strings.HasSuffix(file, "_test.go") {
			continue // where the palette is defined, and its string form
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(source), `lipgloss.Color("#`) ||
			strings.Contains(string(source), `lipgloss.AdaptiveColor{`) {
			t.Errorf("%s writes a colour of its own; put it in theme.go", file)
		}
	}
}

// A question and its answer are two things, not one paragraph: a blank line
// between them, and another before the next question.
func TestQuestionAndAnswerAreSpaced(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.agent.Provider = stubProvider{}
	m.lines, m.chatted = nil, false

	m.input.SetValue("hi")
	m.submit()
	if len(m.lines) != 2 || m.lines[0].kind != entryAsk || strings.TrimSpace(m.lines[1].text) != "" {
		t.Fatalf("a question is followed by a blank line: %+v", m.lines)
	}

	// The next question opens with one of its own, so turns do not run together.
	m.input.SetValue("lagi")
	m.submit()
	if strings.TrimSpace(m.lines[2].text) != "" || m.lines[3].kind != entryAsk {
		t.Fatalf("the next turn starts with a blank line: %+v", m.lines[2:])
	}
}

// A diff without line numbers is half an answer: you can see what changes and
// not where. The provider never says where — it sends the text to replace, not
// its position — so the number is counted from the file itself.
func TestDiffCarriesLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	source := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}\n"
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	// The line to replace is the sixth.
	if got := lineOf(path, "\tfmt.Println(\"Hello\")"); got != 6 {
		t.Fatalf("lineOf = %d, want 6", got)
	}

	detail := confirmDetail("edit_file", fmt.Sprintf(
		`{"path":%q,"old":"\tfmt.Println(\"Hello\")","new":"\tfmt.Println(\"Halo\")"}`, path))
	if !strings.Contains(detail, "6 -") || !strings.Contains(detail, "6 +") {
		t.Fatalf("the replaced line keeps its number on both sides:\n%s", detail)
	}

	// Text the file does not contain has no line to give, and the diff says
	// so by leaving the column empty rather than inventing a number.
	if got := lineOf(path, "sesuatu yang tidak ada"); got != 0 {
		t.Fatalf("lineOf = %d, want 0", got)
	}
	if strings.Contains(diff("a", "b", 0), "0 -") {
		t.Fatal("an unknown position must not be printed as line zero")
	}
}

// A line too long for the screen keeps its margin: the continuations tuck in
// under it rather than falling back to the left edge, where they read as
// something new having been said.
func TestLongLinesHangRatherThanFallLeft(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m.lines = nil

	m.addHistory("error: provider error (HTTP 429): rate limit reached for this model in this organization on tokens per minute")
	rows := strings.Split(strings.TrimRight(m.chat.View(), "\n"), "\n")

	var used []string
	for _, row := range rows {
		if strings.TrimSpace(row) != "" {
			used = append(used, row)
		}
	}
	if len(used) < 2 {
		t.Fatalf("the line must wrap at this width: %q", used)
	}
	for i, row := range used {
		if visibleLen(row) > m.cols() {
			t.Errorf("row %d is %d columns, the chat is %d", i, visibleLen(row), m.cols())
		}
		if i > 0 && !strings.HasPrefix(row, "  ") {
			t.Errorf("continuation %d fell back to the edge: %q", i, row)
		}
	}
}

// A tool call is a note of what happened: one line, cut to fit, never two.
func TestToolLinesFitOnOneLine(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	m.lines = nil

	m.Update(teaToolMsg(`run_bash find . -maxdepth 3 -name "*.go" -not -path "*/node_modules/*" -not -path "*/.git/*"; echo done`))

	var used []string
	for _, row := range strings.Split(m.chat.View(), "\n") {
		if strings.TrimSpace(row) != "" {
			used = append(used, row)
		}
	}
	if len(used) != 1 {
		t.Fatalf("a tool call takes one row, got %d: %q", len(used), used)
	}
	if !strings.HasSuffix(strings.TrimSpace(used[0]), "…") {
		t.Fatalf("and says it was cut: %q", used[0])
	}
}

// The welcome box is drawn to the full width, which makes it the first thing
// to break when wrapping changes: a line that already fits must be left alone,
// and one that does not must be cut rather than allowed to push a wall off.
func TestWelcomeBoxStaysABox(t *testing.T) {
	for _, width := range []int{40, 60, 100} {
		m := newTeaModel(agent.New(nil), nil)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 16})

		var box []string
		for _, row := range strings.Split(m.chat.View(), "\n") {
			if strings.TrimSpace(row) != "" {
				box = append(box, row)
			}
		}
		// A rule, four lines, a rule.
		if len(box) != 6 {
			t.Fatalf("width %d: the box is six rows, got %d:\n%s", width, len(box), strings.Join(box, "\n"))
		}
		for i, row := range box {
			if got := visibleLen(row); got != m.cols() {
				t.Errorf("width %d row %d is %d columns, want %d: %q", width, i, got, m.cols(), row)
			}
		}
		if !strings.HasPrefix(box[0], dim+"╭") || !strings.Contains(box[len(box)-1], "╯") {
			t.Errorf("width %d: the box lost a corner:\n%s", width, strings.Join(box, "\n"))
		}
	}

	// Cutting counts columns, not characters: a coloured line is mostly
	// escapes, and counting those would leave a fraction of the text.
	coloured := teaDim.Render(strings.Repeat("panjang ", 20))
	if got := visibleLen(cutVisible(coloured, 30)); got != 30 {
		t.Errorf("cut to %d columns, want 30", got)
	}

	// A path longer than the box is cut, not allowed to burst it.
	long := strings.Repeat("/sangat-panjang", 12)
	rows := boxLines(50, "cwd: "+long)
	for _, row := range rows {
		if visibleLen(row) != 50 {
			t.Fatalf("a long line burst the box: %d columns", visibleLen(row))
		}
	}
}

// The picker used to be pinned to twelve rows while each item took three of
// them, so a screen offered one model at a time and choosing meant scrolling
// blind. It must use the screen it was given.
func TestPickerShowsMoreThanOneModelAtATime(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 76, Height: 24})

	var items []list.Item
	for _, name := range []string{"a/one", "a/two", "b/three", "b/four", "c/five", "c/six", "c/seven", "c/eight"} {
		items = append(items, teaItem{title: name, desc: "200k context · $3.00/$15.00 per Mtok"})
	}
	m.picker = m.newPicker(items, "Choose model")
	m.mode = teaModelPicker

	shown := 0
	for _, item := range items {
		if strings.Contains(m.View(), item.(teaItem).title) {
			shown++
		}
	}
	if shown < 6 {
		t.Fatalf("only %d of %d models fit on a 24-row screen", shown, len(items))
	}
}

// The command menu drew every match without being counted in the block under
// the chat, so eleven commands made the view taller than the terminal. The
// rows that fell off took the selection with them: pressing up at the first
// command wrapped to the last, which was no longer on the screen.
func TestCommandMenuKeepsTheSelectionOnScreen(t *testing.T) {
	for _, height := range []int{14, 24, 40} {
		m := newTeaModel(agent.New(nil), nil)
		m.Update(tea.WindowSizeMsg{Width: 76, Height: height})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})

		menu := matches(m.input.Value())
		if len(menu) < 5 {
			t.Fatalf("expected the whole command list, got %d", len(menu))
		}
		for step := 0; step <= len(menu); step++ {
			view := m.View()
			if rows := strings.Count(view, "\n") + 1; rows > height {
				t.Fatalf("height=%d step=%d: view is %d rows, taller than the screen", height, step, rows)
			}
			if selected := menu[m.commandSel].name; !strings.Contains(view, selected+" ") {
				t.Fatalf("height=%d step=%d: %s is selected but not drawn", height, step, selected)
			}
			m.Update(tea.KeyMsg{Type: tea.KeyUp}) // wraps at the top
		}
	}
}

// Anthropic keys that are linked to an identity rather than to one workspace
// have to say which workspace a request acts in, so /connect asks for it after
// the key — and connects anyway when there is nothing to give.
func TestConnectAsksForTheWorkspaceAfterTheKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_WORKSPACE_ID", "OUHAI_API_KEY", "OUHAI_MODEL"} {
		t.Setenv(env, "")
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("anthropic")
	if m.credField != config.FieldKey {
		t.Fatalf("the key comes first, got %q", m.credField)
	}

	m.saveCredential("sk-test")
	if m.credField != config.FieldWorkspace {
		t.Fatalf("the workspace id is asked for next, got %q", m.credField)
	}
	if view := m.View(); !strings.Contains(view, "workspace id") || !strings.Contains(view, "esc connects without it") {
		t.Fatalf("the question must name the field and say it is optional:\n%s", view)
	}

	m.saveCredential("wrkspc_1")
	if got := config.Workspace("anthropic"); got != "wrkspc_1" {
		t.Fatalf("workspace id = %q", got)
	}
	// Saving the second field must not have erased the first.
	if got := config.APIKey("anthropic"); got != "sk-test" {
		t.Fatalf("the key was lost saving the workspace: %q", got)
	}

	// A provider with no extras connects as soon as the key is in.
	m.selectProvider("groq")
	m.saveCredential("gsk-test")
	if m.mode == teaKeyEntry {
		t.Error("groq has nothing to ask after the key")
	}
}

// A failed task reports nothing, so /tasks used to show only the error — and
// five minutes of work went with it. What it was writing when it died is the
// only account of that work.
func TestFailedTaskShowsWhatItWrote(t *testing.T) {
	a := agent.New(nil)
	done, _, err := a.Tasks.Run(context.Background(), "review the diff", func(ctx context.Context, tk task.Task) (string, int, error) {
		a.Tasks.Progress(tk.ID, "found three problems in tea.go")
		return "", 0, errors.New("provider error (HTTP 429): overloaded")
	})
	if err == nil {
		t.Fatal("the task was supposed to fail")
	}

	report := tasksReport(a, done.ID)
	if !strings.Contains(report, "failed") || !strings.Contains(report, "429") {
		t.Errorf("the reason must be there: %q", report)
	}
	if !strings.Contains(report, "found three problems in tea.go") {
		t.Errorf("the partial answer must be there: %q", report)
	}
}

// The id is printed on the way out, which is the moment it is needed — but
// only when something was actually saved to resume.
func TestSessionIDIsOnlyOfferedWhenThereIsOne(t *testing.T) {
	session = config.Session{Started: time.Now()}
	if got := savedSessionID(); got != "" {
		t.Fatalf("nothing was said, so there is nothing to resume: %q", got)
	}

	session.Messages = []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}},
	}}
	if got := savedSessionID(); got != session.Started.Format("2006-01-02T15-04-05") {
		t.Fatalf("the id is the moment it started, got %q", got)
	}
}

// A thinking model can be quiet for twenty seconds before it writes anything.
// The working out is shown while it is all there is, then collapses to a line:
// keeping it would bury the answer, dropping it would leave the wait
// unexplained.
func TestThinkingIsShownThenCollapsed(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.Update(teaThinkMsg("the file is probably main.go"))
	rows := strings.Join(m.streamRows(), "\n")
	if !strings.Contains(rows, "the file is probably main.go") {
		t.Fatalf("the working out must be visible while it is all there is: %q", rows)
	}

	m.Update(teaDeltaMsg("Reading main.go"))
	if got := strings.Join(m.streamRows(), "\n"); strings.Contains(got, "probably") {
		t.Fatalf("the answer starting must end the thinking: %q", got)
	}
	var note string
	for _, line := range m.lines {
		if strings.Contains(line.text, "thought for") {
			note = line.text
		}
	}
	if note == "" {
		t.Fatal("the wait must be accounted for, even after the thinking goes")
	}
}
