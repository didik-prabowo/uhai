package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/didik-prabowo/uhai/internal/daemon"
	"github.com/didik-prabowo/uhai/internal/session/filestore"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/provider/anthropic"
	"github.com/didik-prabowo/uhai/internal/session"
	"github.com/didik-prabowo/uhai/internal/task"
	"github.com/didik-prabowo/uhai/internal/tools"
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
	m.changeModel("nope/not-a-model", true) // even a model that cannot load
	if m.mode != teaPrompt {
		t.Fatalf("after choosing a model the picker must close, mode = %v", m.mode)
	}

	m.mode = teaProviderPicker
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	if !strings.Contains(m.View().Content, "halo dunia") {
		t.Fatalf("the answer must appear while it streams:\n%s", m.View().Content)
	}
	if m.streamed != len("halo dunia") {
		t.Fatalf("streamed chars = %d", m.streamed)
	}

	// Once complete the answer leaves the live block and is printed into the
	// terminal's scrollback instead — here, with no program running, that is
	// the pending history.
	// Two lines: the answer, and the blank one that keeps it clear of
	// whatever was said above it.
	m.Update(teaTextMsg("halo dunia"))
	if m.stream != "" || len(m.lines) != printed+2 {
		t.Fatalf("finished answer: stream = %q, history = %q", m.stream, m.lines)
	}
	// The live block itself, not the whole View: View draws the pending
	// history too, so the answer is legitimately somewhere in it. This
	// assertion used to read View and passed only because glamour's escapes
	// happened to break "halo dunia" into pieces that Contains could not find
	// — true for the wrong reason, and it stopped being true the moment the
	// renderer and lipgloss agreed on a colour profile.
	if rows := m.streamRows(); len(rows) != 0 {
		t.Fatalf("the finished answer must not stay in the live block: %q", rows)
	}
}

func TestTypingWhileBusyIsQueued(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.busy = true

	m.Update(tea.KeyPressMsg{Text: "halo"})
	if m.input.Value() != "halo" {
		t.Fatalf("the prompt must accept typing while the model works, got %q", m.input.Value())
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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

	rows := strings.Split(m.View().Content, "\n")
	if len(rows) != 24 {
		t.Fatalf("the screen must be filled exactly, got %d rows", len(rows))
	}
	if !strings.Contains(rows[23], "─") {
		t.Fatalf("the form must sit on the last row, got %q", rows[23])
	}

	for i := 0; i < waitTries; i++ {
		m.addHistory(fmt.Sprintf("line %d", i))
	}
	bottom := m.chat.YOffset()

	// Shift with the arrows scrolls; the arrows alone are the prompt's
	// history, so the wheel can never type into the box.
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if m.chat.YOffset() >= bottom {
		t.Fatalf("shift+up must scroll the chat, offset stayed at %d", m.chat.YOffset())
	}
	scrolled := m.chat.YOffset()
	// The page keys are history, not a second way to scroll: on a Mac they
	// are fn with the arrows, which the hand reads as the same key.
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.chat.YOffset() != scrolled {
		t.Fatalf("pgup must not scroll the chat, offset moved to %d", m.chat.YOffset())
	}
	if got := strings.Split(m.View().Content, "\n"); !strings.Contains(got[23], "─") {
		t.Fatalf("scrolling must not move the form, last row is %q", got[23])
	}

	// Typing is not scrolling, and it does not follow the chat back down.
	for _, key := range []string{"j", "k", "f", "b", "u", "d", " "} {
		m.Update(tea.KeyPressMsg{Text: key})
	}
	if m.chat.YOffset() != scrolled {
		t.Fatalf("typing must not scroll the chat, offset moved %d → %d", scrolled, m.chat.YOffset())
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
	if m.chat.Height() != 20-m.blockHeight() {
		t.Fatalf("the chat must take what the prompt leaves, got %d", m.chat.Height())
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
		m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
		if m.input.Value() != want {
			t.Fatalf("pgup %d gave %q, want %q", i+1, m.input.Value(), want)
		}
	}
	for i, want := range []string{"tiga", "empat", "lima", "enam", "draf", "draf"} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
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

	bottom := m.chat.YOffset()
	m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	up := m.chat.YOffset()
	if up >= bottom {
		t.Fatalf("ctrl+y must scroll up, offset stayed at %d", up)
	}
	if m.input.Value() != "" {
		t.Fatalf("scrolling must leave the prompt alone, got %q", m.input.Value())
	}
	m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if m.chat.YOffset() <= up {
		t.Fatalf("ctrl+e must scroll back down, offset stayed at %d", m.chat.YOffset())
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
	bottom := m.chat.YOffset()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.chat.YOffset() >= bottom {
		t.Fatalf("the wheel must scroll the chat, offset stayed at %d", m.chat.YOffset())
	}
	// /mouse hands the mouse over whole, for a terminal whose shift-drag is
	// not enough, and says so while it is gone.
	m.input.SetValue("/mouse")
	m.submit()
	if m.wheel {
		t.Fatal("/mouse must hand the mouse back")
	}
	// v2 carries the mouse mode on the view rather than in a command, so this
	// is where handing it back is now visible.
	if mode := m.View().MouseMode; mode != tea.MouseModeNone {
		t.Fatalf("the view must stop asking for the mouse, mode = %v", mode)
	}
	if !strings.Contains(m.View().Content, "the terminal's") {
		t.Fatalf("the status row must say the mouse is gone:\n%s", m.View().Content)
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
	saved, err := session.Latest(store)
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

	if got := confirmDetail("run_bash", `{"command":"go test ./..."}`); !strings.Contains(plain(got), "$ go test ./...") {
		t.Fatalf("a command must be shown as one: %q", got)
	}
}

// "always" used to approve one call and ask again on the next.
func TestAlwaysIsRemembered(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	reply := make(chan bool, 1)
	m.Update(teaConfirmMsg{request: teaConfirm{name: "run_bash", input: `{"command":"ls"}`, reply: reply}})
	m.Update(tea.KeyPressMsg{Text: "a"})
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
	view := plain(m.View().Content)
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
// A gateway that wraps every turn in an empty <think></think> — 9router's
// cc/* route does — was sending a block that is text to the agent and nothing
// to the eye. It rendered to no rows, and the blank line an answer is given
// was all that survived: one gap per turn, splitting tool calls that belonged
// together.
func TestAnAnswerThatRendersToNothingAddsNoLines(t *testing.T) {
	m := newTeaModel(agent.New(stubProvider{}), nil)
	m.width = 80
	m.add(chatEntry{kind: entryAnswer, text: "sebelumnya"})
	before := len(m.lines)

	m.Update(teaTextMsg("<think></think>"))
	if len(m.lines) != before {
		t.Fatalf("an empty thinking block is not a line of the chat: %d -> %d", before, len(m.lines))
	}

	// One with something in it still is, working out and all.
	m.Update(teaTextMsg("<think>hm</think>jawaban"))
	if len(m.lines) == before {
		t.Fatal("an answer with words in it has to be drawn")
	}
}

// Attached, the turn runs against the daemon's agent, and so must the model.
// /model used to swap this process's provider — the one nothing asks — and the
// status row read that same provider, so the row named a model no turn had run
// against while the answers kept coming back in the old model's voice. Nothing
// on screen contradicted it.
func TestAttachedSendsTheModelSwitchToTheDaemon(t *testing.T) {
	m := newTeaModel(agent.New(stubProvider{}), nil)
	m.width = 80
	attached, attachedModel = daemon.DialFor(filepath.Join(t.TempDir(), "d.sock"), t.TempDir()), "9router/scan-cheap"
	t.Cleanup(func() { attached, attachedModel = nil, "" })

	if got := m.modelHint(); !strings.Contains(got, "9router/scan-cheap") {
		t.Fatalf("the row must name the model that answers, got %q", got)
	}

	// The switch leaves as a command and nothing local moves: the local
	// provider is not what answers, so changing it would be the old bug.
	if cmd := m.changeModel("9router/cc/claude-opus-5", false); cmd == nil {
		t.Fatal("attached, a model change has to be sent to the daemon")
	}
	if name := m.agent.Provider.Name(); name != "stub/stub" {
		t.Fatalf("nothing local may change while the daemon is answering, got %s", name)
	}

	// The daemon's answer, and only then does the row change.
	m.Update(teaAttachedModelMsg{name: "9router/cc/claude-opus-5"})
	if got := m.modelHint(); !strings.Contains(got, "9router/cc/claude-opus-5") {
		t.Fatalf("the row follows the daemon, got %q", got)
	}

	// A refusal keeps the model that works and says which one that is.
	m.Update(teaAttachedModelMsg{err: errors.New("no such model")})
	if attachedModel != "9router/cc/claude-opus-5" {
		t.Fatalf("a refused switch must change nothing, got %q", attachedModel)
	}
	if last := plain(m.lines[len(m.lines)-1].text); !strings.Contains(last, "no such model") {
		t.Fatalf("a refusal has to say why: %q", last)
	}

	// /connect is still refused: it writes a key, and the daemon's own
	// settings are what it would have to be written into.
	m.beginConnect("")
	if last := plain(m.lines[len(m.lines)-1].text); !strings.Contains(last, "daemon-stop") {
		t.Fatalf("connecting while attached has to say how to do it: %q", last)
	}
}

// Attached, the history that matters is the daemon's. The opening line counted
// agent.History, which is empty in an attached front end however long the
// daemon's conversation is — so a daemon that had picked the morning back up
// off disk was drawn as a blank screen saying nothing, and the next prompt was
// answered out of a context nobody on this side could see.
func TestAttachedSaysWhatTheDaemonPickedUp(t *testing.T) {
	attached = daemon.DialFor(filepath.Join(t.TempDir(), "d.sock"), t.TempDir())
	attachedHolding = daemon.Holding{Model: "9router/scan-cheap", Resumed: 12, ResumedTokens: 3400}
	t.Cleanup(func() { attached, attachedHolding = nil, daemon.Holding{} })

	m := newTeaModel(agent.New(stubProvider{}), nil)
	m.width = 80
	if len(m.opening) == 0 || !strings.Contains(plain(m.opening[0]), "12 messages") {
		t.Fatalf("attached, the opening line has to name the daemon's history: %v", m.opening)
	}

	// /clear is refused rather than emptying a local agent that answers
	// nothing. A command that looks like it worked and changed nothing is the
	// same bug wearing different clothes.
	m.slashCommand("/clear")
	if last := plain(m.lines[len(m.lines)-1].text); !strings.Contains(last, "the daemon's") {
		t.Fatalf("/clear attached has to say it cannot: %q", last)
	}
}

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
	// Isolated from whatever daemon the machine happens to be running. /bg
	// prefers one when it is there, so without this the test's result depends
	// on the developer's own processes — and it did: a stray daemon took the
	// task, ran it, and left this registry empty.
	t.Setenv("HOME", t.TempDir())

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
	for _, env := range []string{"OPENAI_API_KEY", "UHAI_API_KEY", "UHAI_MODEL"} {
		t.Setenv(env, "")
	}
	if err := config.Save("openai", config.Creds{config.FieldKey: "kunci-lama-yang-salah"}); err != nil {
		t.Fatal(err)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("openai")
	if m.mode != teaKeyEntry {
		t.Fatal("a provider with a saved key must still offer to take a new one")
	}
	if !strings.Contains(m.keyInput.Placeholder, "keep the one saved") {
		t.Fatalf("the prompt must say what escape does, got %q", m.keyInput.Placeholder)
	}

	// Escaping keeps what is stored rather than throwing the connection away.
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := config.APIKey("openai"); got != "kunci-lama-yang-salah" {
		t.Fatalf("escape must keep the saved key, got %q", got)
	}

	// Typing one replaces it.
	m.selectProvider("openai")
	m.keyInput.SetValue("kunci-baru")
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := config.APIKey("openai"); got != "kunci-baru" {
		t.Fatalf("a new key must replace the old one, got %q", got)
	}
}

// The environment beats the file, so saving a key while one is exported looks
// like nothing happened unless it is said.
func TestConnectWarnsWhenTheEnvironmentWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "dari-env")

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("openai")
	m.keyInput.SetValue("dari-ketikan")
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEnter})

	var said bool
	for _, e := range m.lines {
		said = said || strings.Contains(e.text, "OPENAI_API_KEY is set")
	}
	if !said {
		t.Fatalf("the environment winning must be said out loud: %+v", m.lines)
	}
}

// Nobody remembers a console URL, and leaving to look it up loses the prompt.
func TestKeyEntryShowsWhereToGetOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"ANTHROPIC_API_KEY", "UHAI_API_KEY", "UHAI_MODEL"} {
		t.Setenv(env, "")
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider("anthropic")

	view := m.View().Content
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
	if !strings.Contains(m.View().Content, "esc keeps the key already saved") {
		t.Fatalf("escape must say what it keeps:\n%s", m.View().Content)
	}
}

// Patterns decide per command, which is the only useful shape for a shell:
// "git *" allowed and "git push *" denied is a rule a person actually wants.
func TestCommandPatternsDecideBeforeAnyoneIsAsked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"allow":["Bash(git:*)"],"deny":["Bash(git push:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"), []byte(rules), 0o644); err != nil {
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
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !strings.Contains(m.View().Content, "❯ 3. No") {
		t.Fatalf("the cursor must move:\n%s", m.View().Content)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if <-reply {
		t.Fatal("the third answer is no")
	}

	// A number answers straight away, and 2 remembers the tool.
	reply = ask()
	m.Update(tea.KeyPressMsg{Text: "2"})
	if !<-reply {
		t.Fatal("the second answer is yes")
	}
	if !m.allowed.has("run_bash") {
		t.Fatal("and it is the one that remembers")
	}

	// The letters still work, for hands that already know them.
	reply = ask()
	m.Update(tea.KeyPressMsg{Text: "n"})
	if <-reply {
		t.Fatal("n is still no")
	}

	// Escape is no, not a way to leave the question unanswered — the tool call
	// is waiting on the other end of that channel.
	reply = ask()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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

	rows := strings.Split(m.View().Content, "\n")
	if len(rows) != 24 {
		t.Fatalf("the view must still be one screen, got %d rows", len(rows))
	}
	if !strings.Contains(m.View().Content, "baris 29") {
		t.Fatalf("the newest chat line must survive the panel:\n%s", m.View().Content)
	}
	if !strings.Contains(rows[len(rows)-1], "╰") {
		t.Fatalf("and the panel must end the block, last row is %q", rows[len(rows)-1])
	}
}

// Claude writes a sentence as text beside a tool call and GLM puts the same
// sentence in its reasoning, which goes when the answer starts — so the tool
// block had nothing above it saying why, on exactly the models that explain
// themselves the least.
func TestTheReasoningLeavesASentenceAboveTheToolCalls(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.Update(teaThinkMsg("Perlu lihat repository-nya.\nCari pola sq.Update dulu."))
	m.Update(teaToolMsg(toolLine(tools.NameGrep, `{"pattern":"sq.Update"}`)))

	rows := make([]string, 0, 2)
	for _, line := range m.lines[len(m.lines)-2:] {
		rows = append(rows, strings.TrimSpace(plain(line.text)))
	}
	want := []string{"Cari pola sq.Update dulu.", "⎿ Searching sq.Update"}
	if !slices.Equal(rows, want) {
		t.Fatalf("the last thought stands above the call, got %q want %q", rows, want)
	}
	// Once said it is not said again: the second call in the same batch has
	// the same working out behind it.
	m.Update(teaToolMsg(toolLine(tools.NameGrep, `{"pattern":"SetMap"}`)))
	if got := strings.TrimSpace(plain(m.lines[len(m.lines)-2].text)); got != "⎿ Searching sq.Update" {
		t.Fatalf("the sentence must not repeat, got %q above the second call", got)
	}
}

// A turn that ran tools ends with the answer, and it used to be appended
// straight after the last ⎿ line — so the reply looked like one more of them.
func TestTheAnswerStandsClearOfTheToolCalls(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.Update(teaToolMsg(toolLine(tools.NameGrep, `{"pattern":"SELECT"}`)))
	m.Update(teaTextMsg("Scan singkat."))

	last := m.lines[len(m.lines)-1]
	gap := m.lines[len(m.lines)-2]
	if last.kind != entryAnswer || strings.TrimSpace(gap.text) != "" {
		t.Fatalf("the answer needs a blank line above it, got %+v then %+v", gap, last)
	}
	// And only one: a question already leaves a line below it, and two blanks
	// read as the turn losing its place.
	m2 := newTeaModel(agent.New(nil), nil)
	m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	was := len(m2.lines)
	m2.addHistory("")
	m2.Update(teaTextMsg("Halo."))
	if len(m2.lines) != was+2 {
		t.Fatalf("a blank line already there is enough, got %d new lines", len(m2.lines)-was)
	}
}

// A tool call is a line of the conversation, so it has to read like one: a
// verb and what it is happening to, not the model's name for the tool. A case
// added to toolLine without a verb would draw a line starting with a space.
func TestToolCallsReadAsWhatTheyDo(t *testing.T) {
	for _, c := range []struct{ name, input, want string }{
		{tools.NameBash, `{"command":"go run hello-world/main.go"}`, "Running go run hello-world/main.go"},
		{tools.NameEdit, `{"path":"hello-world/main.go","old":"a","new":"b"}`, "Editing hello-world/main.go"},
		{tools.NameWrite, `{"path":"a.go","content":"x"}`, "Writing a.go"},
		{tools.NameRead, `{"path":"internal/cli/tea.go"}`, "Reading internal/cli/tea.go"},
		{tools.NameFetch, `{"url":"https://example.com"}`, "Fetching https://example.com"},
		{tools.NameGrep, `{"pattern":"func Test","include":"*.go","path":"internal"}`, "Searching func Test in *.go under internal"},
		{tools.NameGlob, `{"pattern":"**/*_test.go"}`, "Finding **/*_test.go"},
		// Nothing to read the arguments out of, so the name is all there is.
		{tools.NameRead, `not json at all`, "read_file not json at all"},
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

	rows := strings.Split(m.View().Content, "\n")
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
	if !strings.HasSuffix(strings.TrimSpace(plain(used[0])), "…") {
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
		if strings.Contains(m.View().Content, item.(teaItem).title) {
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
		m.Update(tea.KeyPressMsg{Text: "/"})

		menu := matches(m.input.Value())
		if len(menu) < 5 {
			t.Fatalf("expected the whole command list, got %d", len(menu))
		}
		for step := 0; step <= len(menu); step++ {
			view := m.View().Content
			if rows := strings.Count(view, "\n") + 1; rows > height {
				t.Fatalf("height=%d step=%d: view is %d rows, taller than the screen", height, step, rows)
			}
			if selected := menu[m.commandSel].name; !strings.Contains(view, selected+" ") {
				t.Fatalf("height=%d step=%d: %s is selected but not drawn", height, step, selected)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // wraps at the top
		}
	}
}

// Anthropic keys that are linked to an identity rather than to one workspace
// have to say which workspace a request acts in, so /connect asks for it after
// the key — and connects anyway when there is nothing to give.
func TestConnectAsksForTheWorkspaceAfterTheKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_WORKSPACE_ID", "UHAI_API_KEY", "UHAI_MODEL"} {
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
	if view := m.View().Content; !strings.Contains(view, "workspace id") || !strings.Contains(view, "esc connects without it") {
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
	m.selectProvider("openai")
	m.saveCredential("gsk-test")
	if m.mode == teaKeyEntry {
		t.Error("openai has nothing to ask after the key")
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
	resetSessions(t)
	if got := savedSessionID(); got != "" {
		t.Fatalf("nothing was said, so there is nothing to resume: %q", got)
	}

	UseStore(filestore.New(t.TempDir()))
	a := agent.New(nil)
	a.History = []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}},
	}}
	if err := SaveSession(a); err != nil {
		t.Fatal(err)
	}
	// The id names the file that is actually on disk — this project's, which
	// since the daemon began serving several is a lookup rather than a global.
	saved, err := session.LatestIn(store, here())
	if err != nil {
		t.Fatal(err)
	}
	if got := savedSessionID(); got != saved.ID {
		t.Fatalf("the id offered must be the one written, got %q want %q", got, saved.ID)
	}
}

// A thinking model can be quiet for twenty seconds before it writes anything.
// The working out is shown while it is all there is and goes when the answer
// starts: keeping it would bury the answer, and a note in its place is a
// leftover of the wait rather than part of the reply.
func TestThinkingIsShownThenGone(t *testing.T) {
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
	for _, line := range m.lines {
		if strings.Contains(line.text, "thought") {
			t.Fatalf("the wait must leave nothing behind: %q", line.text)
		}
	}
}

// The diff is the whole reason a confirmation is worth reading, so it has to
// show the change and not the block around it. This used to match only the
// common head and tail, which made one changed line in the middle read as the
// whole function being replaced — and a diff that cries wolf is answered with
// y without being read.
func TestDiffShowsTheChangedLinesOnly(t *testing.T) {
	before := strings.Join([]string{
		"func handler(w http.ResponseWriter, r *http.Request) {",
		"\tuser, err := auth.User(r.Context())",
		"\tif err != nil {",
		"\t\thttp.Error(w, \"unauthorized\", 401)",
		"\t\treturn",
		"\t}",
		"\trender(w, user)",
		"}",
	}, "\n")
	after := strings.ReplaceAll(before, "401", "http.StatusUnauthorized")

	got := diff(before, after, 1)
	removed, added := strings.Count(got, "-"), strings.Count(got, "+")
	if removed == 0 || added == 0 {
		t.Fatalf("a change needs both sides:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "func handler") && (strings.Contains(line, "-") || strings.Contains(line, "+")) {
			t.Fatalf("an untouched line was reported as changed:\n%s", got)
		}
	}
	if !strings.Contains(got, "http.StatusUnauthorized") {
		t.Fatalf("the new line must be there:\n%s", got)
	}
	// Only the one line changed, so only one line may be marked on each side.
	if n := strings.Count(got, "401"); n != 1 {
		t.Fatalf("the old line should appear once, appeared %d times:\n%s", n, got)
	}
}

// A file with nothing in common with its replacement is a rewrite, and reads
// as one: everything out, everything in.
func TestDiffOfARewrite(t *testing.T) {
	got := diff("one\ntwo\nthree", "alpha\nbeta", 1)
	for _, want := range []string{"one", "two", "three", "alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from a rewrite:\n%s", want, got)
		}
	}
}

// Lines far from any change are skipped, and the gap is marked — otherwise the
// numbers jump and look like a mistake.
func TestDiffSkipsFarAwayLines(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	before := strings.Join(lines, "\n")
	lines[20] = "line twenty, changed"
	after := strings.Join(lines, "\n")

	got := diff(before, after, 1)
	if strings.Contains(got, "line 0") {
		t.Fatalf("a line forty rows from the change is not context:\n%s", got)
	}
	if !strings.Contains(got, "⋮") {
		t.Fatalf("a skipped stretch has to be visible:\n%s", got)
	}
	if !strings.Contains(got, "line 18") || !strings.Contains(got, "line 22") {
		t.Fatalf("two lines either side are context:\n%s", got)
	}
}

// Without a terminal a slash command used to be sent to the model as if it
// were a question: a turn spent, nothing answered.
func TestPipeAnswersCommandsItself(t *testing.T) {
	help := pipeAnswer("/help")
	if !strings.Contains(help, "/connect") || !strings.Contains(help, "one prompt per line") {
		t.Fatalf("/help must list the commands and say how this mode works:\n%s", help)
	}

	for _, prompt := range []string{"/connect", "/model zai/glm-4.7", "/tasks t1"} {
		got := pipeAnswer(prompt)
		if !strings.Contains(got, "interactive prompt") {
			t.Errorf("%q should be refused by name, got %q", prompt, got)
		}
		if strings.Contains(got, "/connect ") && strings.Contains(prompt, "/model") {
			t.Errorf("the refusal must name the command asked for: %q", got)
		}
	}

	// /skills is the exception: it reads the disk and prints, so refusing it
	// would put the only way to check a skill was found behind a terminal —
	// which is where a script cannot look.
	if got := pipeAnswer("/skills"); strings.Contains(got, "interactive prompt") {
		t.Errorf("/skills needs no terminal, got %q", got)
	}
}

// A skill that is in the wrong folder, or whose frontmatter did not parse,
// fails the one way nothing reports: the model just does not follow it. This
// is the only place that says what was found and where it looked.
func TestSkillsReportSaysWhatWasFoundAndWhere(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	// Nothing yet: the answer has to say where to put one, not just "none".
	empty := skillsReport()
	if !strings.Contains(empty, ".uhai/skills") || !strings.Contains(empty, "SKILL.md") {
		t.Errorf("with no skills, say where one would go:\n%s", empty)
	}

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, ".uhai", "skills", "rilis", "SKILL.md"),
		"---\nname: rilis\ndescription: Langkah merilis versi baru\n---\nbadan yang panjang sekali\n")
	write(filepath.Join(dir, ".claude", "skills", "review", "SKILL.md"),
		"---\nname: review\ndescription: Checklist sebelum merge\n---\n")
	write(filepath.Join(dir, ".uhai", "skills", "sunyi", "SKILL.md"), "# tanpa frontmatter\n")

	got := skillsReport()
	// Names, what each costs, and the directory it came from. The question
	// this answers is "what am I paying for and where did it come from" — the
	// description is the model's business, and repeating it here turned four
	// skills into a wall of prose.
	for _, want := range []string{
		"rilis", "review", "sunyi",
		"on ", "tok", ".uhai/skills", ".claude/skills", "looked in",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report must carry %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Langkah merilis versi baru") {
		t.Errorf("descriptions belong in the prompt, not in this list:\n%s", got)
	}
	// A skill with no description is found, paid for on every request, and
	// never opened — the one case worth calling out.
	if !strings.Contains(got, "no description") {
		t.Errorf("a skill without a description must be named as such:\n%s", got)
	}
	// The bodies are the whole reason skills exist: they must not be in here,
	// any more than they are in the prompt.
	if strings.Contains(got, "badan yang panjang") {
		t.Errorf("a skill's body must not be printed:\n%s", got)
	}
	// One row each, then the total and where it looked.
	if lines := strings.Count(got, "\n") + 1; lines != 5 {
		t.Errorf("three skills plus two trailing rows make five lines, got %d:\n%s", lines, got)
	}
	// A personal skill's absolute path is half the terminal and says nothing
	// the ~ does not.
	if strings.Contains(got, dir) {
		t.Errorf("the home directory must read as ~:\n%s", got)
	}
}

// A model is checked against the provider's list after the switch, and only as
// a note: Z.ai answers /models with ten paid models and none of the free ones,
// which work perfectly well. Refusing what a vendor forgot to list would block
// a working model to catch a typo.
func TestUnlistedModelIsANoteNotARefusal(t *testing.T) {
	listed := []string{"glm-4.5", "glm-4.6", "glm-4.7", "glm-5.3"}

	if note := unlistedNote("zai/glm-4.7", listed); note != "" {
		t.Errorf("a listed model needs no note, got %q", note)
	}
	if note := unlistedNote("zai/GLM-4.7", listed); note != "" {
		t.Errorf("the comparison is not case-sensitive, got %q", note)
	}

	// Unlisted but real: said out loud, and not refused.
	note := unlistedNote("zai/glm-4.7-flash", listed)
	if !strings.Contains(note, "glm-4.7-flash") || !strings.Contains(note, "may still work") {
		t.Errorf("an unlisted model should be named and allowed: %q", note)
	}
	// A typo reads the same way, which is the point: one line, no blocking.
	if note := unlistedNote("zai/glm-4.7-flsh", listed); note == "" {
		t.Error("a typo must at least be mentioned")
	}

	// An alias is never in the list itself, only the dated id it points at.
	// Anthropic answers /models that way, and claude-sonnet-5 is what uhai
	// ships as its own default: a note there would fire on the happy path.
	dated := []string{"claude-sonnet-5-20260115", "claude-opus-5-20260115"}
	if note := unlistedNote("anthropic/claude-sonnet-5", dated); note != "" {
		t.Errorf("an alias must count as listed, got %q", note)
	}
	if note := unlistedNote("anthropic/claude-haiku-5", dated); note == "" {
		t.Error("a name no listed id starts with is still worth a note")
	}
}

// Switching a skill off means looking at what it costs first, so it happens in
// the list rather than by editing a file: enter toggles, the row's cost
// changes under the cursor, and the list stays open because switching one off
// is rarely the only one.
func TestSkillsPickerTogglesInPlace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	for _, name := range []string{"gaya", "rilis"} {
		path := filepath.Join(dir, ".uhai", "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: dipakai\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.beginSkills()
	if m.mode != teaSkillPicker {
		t.Fatalf("/skills must open the list, mode = %v", m.mode)
	}
	if n := len(m.picker.Items()); n != 2 {
		t.Fatalf("both skills belong in the list, got %d", n)
	}
	// On is written out, not left as the absence of off: a row whose state has
	// to be worked out is the one thing this list exists to save you.
	if desc := m.picker.Items()[0].(teaItem).desc; !strings.Contains(desc, "on") || !strings.Contains(desc, "tok") {
		t.Errorf("a row must say it is on and what it costs, got %q", desc)
	}

	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEnter})

	// It stays open: switching one off is rarely the only one.
	if m.mode != teaSkillPicker {
		t.Fatalf("the list must stay open after a toggle, mode = %v", m.mode)
	}
	if desc := m.picker.Items()[0].(teaItem).desc; !strings.Contains(desc, "off") {
		t.Errorf("the row must show the skill is now off, got %q", desc)
	}
	if off := config.Skills()[0].Off; !off {
		t.Error("the toggle must reach settings.json, not just the screen")
	}
	// Written to the project, not to the home settings: a skill you carry
	// everywhere is wanted in some repositories and not others.
	saved, err := os.ReadFile(filepath.Join(dir, ".uhai", "settings.json"))
	if err != nil || !strings.Contains(string(saved), "gaya") {
		t.Fatalf("the project's settings must carry it: %s %v", saved, err)
	}

	// And back on again, saying so.
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	if off := config.Skills()[0].Off; off {
		t.Error("enter again must switch it back on")
	}
	if desc := m.picker.Items()[0].(teaItem).desc; !strings.Contains(desc, "on") {
		t.Errorf("the row must say it is on again, got %q", desc)
	}
}

// The status row prices a turn; this prices the conversation. They are not the
// same sum — every call in a turn is charged for its input, and only the last
// one is ever on screen.
func TestCostAddsUpEveryCallNotJustTheLast(t *testing.T) {
	setSpent(here(), nil)
	t.Cleanup(func() { setSpent(here(), nil) })

	if got := spentReport(nil); got != "" {
		t.Errorf("nothing asked yet is nothing to report, got %q", got)
	}

	// One turn, three calls: a tool loop bills its input again every time.
	recordUsage("zai/glm-4.7-flash", provider.Usage{Input: 9000, Output: 40})
	recordUsage("zai/glm-4.7-flash", provider.Usage{Input: 200, CacheRead: 9000, Output: 60})
	recordUsage("zai/glm-4.7-flash", provider.Usage{Input: 250, CacheRead: 9000, Output: 100})

	got := spentReport(nil)
	// 9000 + 200 + 250 + 18000 cached = 27.4k in, 200 out.
	if !strings.Contains(got, "27.4k") || !strings.Contains(got, "200") {
		t.Errorf("every call has to be in the total, got %q", got)
	}
	if !strings.Contains(got, "18.0k from cache") {
		t.Errorf("what came from cache is the difference this session made, got %q", got)
	}
}

// A code block is a box: lined up with the paragraph above it, reaching to one
// column short of the edge, numbered down the side, and in a colour that is
// not the question's. Glamour draws it as flowing text, so uhai draws it.
func TestCodeBlockIsABoxLinedUpWithTheProse(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})

	out := m.rendererText("Contoh:\n\n```go\nfunc main() {\n\tfmt.Println(\"halo\")\n}\n```\n\nSelesai.\n")

	var banded []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "\x1b[48;") {
			banded = append(banded, line)
		}
	}
	// Three lines of code and nothing else banded: the blank rows that used to
	// close the box are gone, since rendererText already leaves a clear line
	// on each side and two rows of nothing above three of code is too much air.
	if len(banded) != 3 {
		t.Fatalf("want three code rows, got %d:\n%s", len(banded), out)
	}

	width := visibleLen(banded[0])
	for _, line := range banded {
		if got := visibleLen(line); got != width {
			t.Errorf("every row of the box is the same width: %d against %d", got, width)
		}
	}
	// Almost the full width, and never all of it: a line as wide as the
	// terminal wraps on its own and scrolls the screen under the renderer.
	if width != m.cols()-1 {
		t.Errorf("the box is %d wide on a %d-column screen, want one short", width, m.cols())
	}
	// And it starts where the prose starts, not against the edge.
	for _, line := range banded {
		if !strings.HasPrefix(line, strings.Repeat(" ", codeIndent)) {
			t.Errorf("the box must line up with the paragraph above it:\n%q", line)
		}
	}
	for i, want := range []string{"1", "2", "3"} {
		if !strings.Contains(plain(banded[i]), want) {
			t.Errorf("row %d is not numbered:\n%q", i, banded[i])
		}
	}
	// A tab is one character and several columns. Left in, the band is drawn
	// one width and the terminal paints another.
	if strings.Contains(out, "\t") {
		t.Error("tabs must be expanded before the block is measured")
	}
}

// The colours were being computed and then thrown away: chroma is given a
// trailing newline and hands back a line more than the source has, the count
// disagreed, and every block fell back to plain text on a band.
func TestCodeBlockIsColoured(t *testing.T) {
	src := "SELECT id\nFROM users;"
	if got, want := len(highlight(src, "sql")), len(strings.Split(src, "\n")); got != want {
		t.Fatalf("highlight returned %d lines for %d of source", got, want)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	out := m.rendererText("```sql\n" + src + "\n```\n")

	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "\x1b[48;") {
			continue // not a row of the box
		}
		// A foreground of chroma's own, and the band still on after it: the
		// two resets are spelled differently and only one was looked for, so
		// the background used to die on the first coloured token.
		if !strings.Contains(line, "\x1b[38;5;") {
			t.Errorf("a row of code carries no colour:\n%q", line)
		}
		if strings.HasSuffix(plain(line), " ") == false {
			t.Errorf("the band must reach the end of the row:\n%q", line)
		}
	}
}

// A line too long for the screen is cut, not wrapped: a wrapped line takes the
// shape of the box with it.
func TestCodeBlockCutsALineTooWideForTheScreen(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})

	long := strings.Repeat("x", 200)
	out := m.rendererText("```\n" + long + "\n```\n")
	for _, line := range strings.Split(out, "\n") {
		if got := visibleLen(line); got > m.cols() {
			t.Fatalf("a row is %d wide on a %d-column screen:\n%q", got, m.cols(), line)
		}
	}
}

// A block needs air. Without a blank line at each edge it sits against the
// sentence that introduced it, and two blocks with one line of prose between
// them read as a single block with a caption inside it.
func TestCodeBlockIsSeparatedFromTheProse(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})

	rows := strings.Split(m.rendererText(
		"Berikut:\n\n```go\nfunc main() {}\n```\n\nJalankan:\n\n```bash\ngo run hello.go\n```\n\nSelesai.\n"), "\n")

	banded := func(i int) bool {
		return i >= 0 && i < len(rows) && strings.Contains(rows[i], "\x1b[48;")
	}
	for i := range rows {
		if !banded(i) {
			continue
		}
		if !banded(i-1) && i > 0 && rows[i-1] != "" {
			t.Errorf("row %d opens a block against prose: %q", i, rows[i-1])
		}
		if !banded(i+1) && i < len(rows)-1 && rows[i+1] != "" {
			t.Errorf("row %d closes a block against prose: %q", i, rows[i+1])
		}
	}
	// Two blocks, so two runs — not one long one with a sentence in the middle.
	runs := 0
	for i := range rows {
		if banded(i) && !banded(i-1) {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("want two separate blocks, got %d", runs)
	}
}

// plain strips the escapes from a rendered line, for the assertions that mean
// "this text is on screen" rather than "these bytes are". lipgloss v1 rendered
// plain when there was no terminal and v2 always renders the colour, so the
// difference used to be invisible in a test and now is not.
func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// The order Z.ai actually returns, oldest first, which is what made the cap
// throw away the newest two.
var zaiModels = []string{
	"glm-4.5", "glm-4.5-air", "glm-4.6", "glm-4.7",
	"glm-5", "glm-5-turbo", "glm-5.1", "glm-5.2", "glm-5.3", "glm-5.3-flash",
}

func TestRecommendedKeepsTheModelsUhaiKnows(t *testing.T) {
	got := recommended("zai", append([]string(nil), zaiModels...))

	// The ones the table knows come first, which is what the picker shows
	// first. Only glm-4.6 has an entry of its own now; the rest land on the
	// "glm-" family, which is a fallback rather than knowledge — the trailing
	// dash is how KnownModel tells them apart.
	if got[0] != "glm-4.6" {
		t.Errorf("the model with its own entry should rank first, got %v", got)
	}

	// And nothing is thrown away, because "/" can only find what is in the
	// list. Ranking used to be a cut, and glm-5 through 5.2 were simply gone.
	if len(got) != len(zaiModels) {
		t.Errorf("want all %d models ranked, got %d: %v", len(zaiModels), len(got), got)
	}
}

// A provider whose models are all strangers keeps the order it sent, so the
// sort cannot make an unknown list worse than it was.
func TestRecommendedLeavesAnUnknownListAlone(t *testing.T) {
	models := []string{"a", "b", "c"}
	if got := recommended("somewhere", models); !slices.Equal(got, models) {
		t.Errorf("want %v, got %v", models, got)
	}
}

// Gemini's own listing, in the order it arrives: forty models alphabetically,
// so the six the picker used to take were three retired 2.5 releases and three
// that cannot hold a conversation at all.
var geminiModels = []string{
	"antigravity-preview-05-2026",
	"deep-research-max-preview-04-2026",
	"embedding-001",
	"gemini-2.5-computer-use-preview-10-2025",
	"gemini-2.5-flash",
	"gemini-2.5-flash-image",
	"gemini-2.5-flash-lite",
	"gemini-2.5-flash-preview-tts",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-3.1-flash-lite",
	"gemini-3.5-flash",
	"gemini-3.5-transcribe",
}

func TestRecommendedSkipsWhatCannotAnswer(t *testing.T) {
	got := recommended("gemini", append([]string(nil), geminiModels...))

	if !slices.Contains(got, "gemini-3.5-flash") {
		t.Errorf("the live default is missing from the picker: %v", got)
	}
	for _, gone := range []string{
		"gemini-2.5-flash",                        // 404 for a new key
		"gemini-2.5-flash-image",                  // draws
		"gemini-2.5-flash-preview-tts",            // speaks
		"gemini-2.5-computer-use-preview-10-2025", // drives a browser
		"gemini-3.5-transcribe",                   // listens
		"embedding-001",                           // measures
		"deep-research-max-preview-04-2026",       // answers in its own time
	} {
		if slices.Contains(got, gone) {
			t.Errorf("%s cannot be chatted with and was offered: %v", gone, got)
		}
	}
}

// A retired model stays usable — it is only unrecommended. Old sessions resume
// on one, and typing its name still works.
func TestRetiredModelsAreOnlyUnrecommended(t *testing.T) {
	if config.Recommendable("gemini/gemini-2.5-flash") {
		t.Error("a retired model should not be suggested")
	}
	if got := config.ContextWindow("gemini/gemini-2.5-flash"); got != 1_000_000 {
		t.Errorf("a retired model must still size correctly, got %d", got)
	}
}

// OpenAI lists every model twice, plain and dated, which spent all six slots
// on three models.
func TestRecommendedCollapsesDatedDuplicates(t *testing.T) {
	got := recommended("openai", []string{
		"gpt-4.1", "gpt-4.1-2025-04-14",
		"gpt-4.1-mini", "gpt-4.1-mini-2025-04-14",
		"gpt-4.1-nano", "gpt-4.1-nano-2025-04-14",
		"gpt-4o", "gpt-4o-mini",
	})
	want := []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Anthropic is the opposite case and the reason the alias has to be present
// before its dated twin is dropped: it lists dated ids and nothing else, so a
// rule that simply removed them would leave the provider empty.
func TestRecommendedKeepsDatedIdsThatStandAlone(t *testing.T) {
	models := []string{"claude-sonnet-5-20260115", "claude-opus-5-20260115"}
	if got := recommended("anthropic", models); !slices.Equal(got, models) {
		t.Errorf("got %v, want all of %v", got, models)
	}
}

// A preview beside its stable release is the same duplication wearing a
// different word, and they stack: -preview-10-2025 is both at once.
func TestUndecorated(t *testing.T) {
	for name, want := range map[string]string{
		"gpt-4.1-2025-04-14":                      "gpt-4.1",
		"claude-sonnet-5-20260115":                "claude-sonnet-5",
		"gemini-3.1-flash-lite-preview":           "gemini-3.1-flash-lite",
		"gemini-2.5-computer-use-preview-10-2025": "gemini-2.5-computer-use",
		"gpt-4o":      "gpt-4o",
		"glm-4.5-air": "glm-4.5-air",
	} {
		if got := undecorated(name); got != want {
			t.Errorf("undecorated(%q) = %q, want %q", name, got, want)
		}
	}
}

// /connect could put a key in and nothing could take one out, so a provider
// stayed in /model for good once it had been tried once.
func TestDisconnectForgetsTheKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"OPENAI_API_KEY", "UHAI_API_KEY", "UHAI_MODEL"} {
		t.Setenv(env, "")
	}
	if err := config.Save("openai", config.Creds{config.FieldKey: "kunci"}); err != nil {
		t.Fatal(err)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.disconnect("openai")
	if got := config.APIKey("openai"); got != "" {
		t.Errorf("the key survived: %q", got)
	}
	if slices.Contains(config.ConnectedProviders(), "openai") {
		t.Error("a provider with no key is not connected")
	}

	// Saying it twice must not claim to have done it twice.
	m.disconnect("openai")
	if !strings.Contains(plain(m.lines[len(m.lines)-1].text), "no saved key") {
		t.Errorf("want a note that there was nothing to forget, got %q", m.lines[len(m.lines)-1].text)
	}
	if m.disconnect("nowhere"); !strings.Contains(plain(m.lines[len(m.lines)-1].text), "unknown provider") {
		t.Errorf("an unknown name must say so, got %q", m.lines[len(m.lines)-1].text)
	}
}

// The environment beats the file, so forgetting the file alone would leave the
// provider working and the message a lie.
func TestDisconnectSaysWhenTheEnvironmentKeepsItAlive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save("openai", config.Creds{config.FieldKey: "kunci"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "dari-env")

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.disconnect("openai")

	var said bool
	for _, e := range m.lines {
		said = said || strings.Contains(plain(e.text), "OPENAI_API_KEY is still set")
	}
	if !said {
		t.Errorf("the environment keeping it alive must be said: %+v", m.lines)
	}
	if config.APIKey("openai") != "dari-env" {
		t.Error("the exported key is not uhai's to remove")
	}
}

// /cost used to start again with the process, so a resumed conversation
// believed it had cost nothing.
func TestSpendSurvivesAResume(t *testing.T) {
	// Before the first recordUsage: the bill lives on the session now, so
	// clearing the sessions afterwards would throw it away.
	resetSessions(t)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	UseStore(filestore.New(filepath.Join(dir, "sessions")))

	recordUsage("anthropic/claude-sonnet-5", provider.Usage{Input: 1_000_000, Output: 100_000})
	recordUsage("anthropic/claude-sonnet-5", provider.Usage{Input: 1_000_000, Output: 100_000})

	a := agent.New(nil)
	// Something has to have been said: a session with no messages is not
	// written at all, which is what "nothing said yet" means on disk.
	a.History = []provider.Message{{Role: provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}}}}
	if err := SaveSession(a); err != nil {
		t.Fatal(err)
	}
	saved, err := session.LatestIn(store, here())
	if err != nil {
		t.Fatal(err)
	}

	// A fresh process knows nothing until the session is handed back.
	setSpent(here(), nil)
	if got := spentReport(nil); got != "" {
		t.Fatalf("a new process has spent nothing, got %q", got)
	}
	ContinueSession(saved)

	got := spentReport(nil)
	if !strings.Contains(got, "2.0M") {
		t.Errorf("the tokens did not come back: %q", got)
	}
	// Two million in and two hundred thousand out of Sonnet 5, at $3/$15.
	if !strings.Contains(got, "$9.00") {
		t.Errorf("want $9.00 priced at Sonnet 5's rates, got %q", got)
	}
}

// A conversation held across two models is priced a share at a time: pricing
// the stored total at whichever model happens to be loaded now is the wrong
// figure the moment /model is used.
func TestSpendIsPricedPerModel(t *testing.T) {
	setSpent(here(), nil)
	t.Cleanup(func() { setSpent(here(), nil) })

	// A million tokens on Sonnet 5 is $3; the same million on a free model is
	// nothing, and must not be billed at Sonnet's rate just for being second.
	recordUsage("anthropic/claude-sonnet-5", provider.Usage{Input: 1_000_000})
	recordUsage("zai/glm-4.7-flash", provider.Usage{Input: 1_000_000})

	got := spentReport(nil)
	if !strings.Contains(got, "$3.00") {
		t.Errorf("want only the paid share billed, got %q", got)
	}
}

// Attached, the answer arrives over a socket instead of through a Go closure,
// and everything downstream has to be unable to tell. A terminal is not
// something these tests can drive, so the translation is checked where it can
// be: as a function.
func TestDaemonEventsBecomeTheSameMessages(t *testing.T) {
	if got := daemonMsg(daemon.Event{Kind: daemon.EventDelta, Text: "ha"}); got != tea.Msg(teaDeltaMsg("ha")) {
		t.Errorf("a delta must arrive as a delta, got %#v", got)
	}
	if got := daemonMsg(daemon.Event{Kind: daemon.EventText, Text: "halo"}); got != tea.Msg(teaTextMsg("halo")) {
		t.Errorf("text must arrive as text, got %#v", got)
	}
	if got := daemonMsg(daemon.Event{Kind: daemon.EventReasoning, Text: "hm"}); got != tea.Msg(teaThinkMsg("hm")) {
		t.Errorf("reasoning is not the answer and must stay apart, got %#v", got)
	}

	// The tool line's whole job is saying which command ran. The event used to
	// carry only the name, so an attached terminal drew "⎿ run_bash" and left
	// out the part worth reading.
	tool := daemonMsg(daemon.Event{Kind: daemon.EventTool, Text: "run_bash", Input: `{"command":"gh pr diff 1"}`})
	if tool != tea.Msg(teaToolMsg("Running gh pr diff 1")) {
		t.Errorf("a tool call must arrive with what it was called with, got %#v", tool)
	}

	// A notice is a note, not an answer. As an answer it went through glamour
	// and cleared the stream with it, so a task reporting in mid-turn wiped
	// the answer being written.
	if got := daemonMsg(daemon.Event{Kind: daemon.EventNotice, Text: "t1 selesai"}); got != tea.Msg(teaNoteMsg("t1 selesai")) {
		t.Errorf("a notice must arrive as a note, got %#v", got)
	}

	usage := daemonMsg(daemon.Event{Kind: daemon.EventUsage, Usage: &daemon.Usage{Input: 12, Output: 3, CacheRead: 900}})
	want := teaUsageMsg(provider.Usage{Input: 12, Output: 3, CacheRead: 900})
	if usage != tea.Msg(want) {
		t.Errorf("usage lost something on the wire: %#v", usage)
	}

	// A usage event with nothing in it is not a usage of zero: it would reset
	// the cost row to nothing halfway through a turn.
	if got := daemonMsg(daemon.Event{Kind: daemon.EventUsage}); got != nil {
		t.Errorf("an empty usage event has nothing to say, got %#v", got)
	}
	// The turn's own bookkeeping is not drawn: the POST returning is what ends
	// a turn here.
	for _, kind := range []string{daemon.EventDone, daemon.EventTaskDone, "something-new"} {
		if got := daemonMsg(daemon.Event{Kind: kind}); got != nil {
			t.Errorf("%s should not be drawn, got %#v", kind, got)
		}
	}
}

// "/" filters the picker, which is bubbletea's own key and needed no code —
// what it needed was for the keys the picker already spends to stop being
// spent while a filter is being typed.
func TestSearchingAPickerKeepsItOpen(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.picker = m.newPicker([]list.Item{
		teaItem{title: "zai/glm-5.3", desc: "1M context"},
		teaItem{title: "anthropic/claude-sonnet-5", desc: "1M context"},
	}, "Choose model")
	m.mode = teaModelPicker

	m.updatePicker(tea.KeyPressMsg{Code: '/', Text: "/"})
	if m.picker.FilterState() == list.Unfiltered {
		t.Fatal(`"/" did not start a search`)
	}

	// A space is a character here, not the skill picker's toggle.
	for _, r := range "son net" {
		m.updatePicker(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// Esc backs out of the search, not out of the picker: a typo must not cost
	// the whole list.
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mode != teaModelPicker {
		t.Errorf("esc closed the picker instead of the search, mode = %v", m.mode)
	}
	if m.picker.FilterState() != list.Unfiltered {
		t.Error("esc left the search running")
	}

	// And with nothing being searched, esc means what it always meant.
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mode != teaPrompt {
		t.Errorf("esc must still close an unsearched picker, mode = %v", m.mode)
	}
}

// A gateway is a base URL and a key, and both used to be a hand edit of two
// files. All three go in on one screen now: they are copied from the same page
// and asking for them one at a time was three enters for one paste.
func TestConnectAddsACustomEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"UHAI_API_KEY", "UHAI_MODEL", "UHAI_BASE_URL"} {
		t.Setenv(env, "")
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// The row is offered, last, and is not mistaken for a provider.
	items := providerItems()
	if last := items[len(items)-1].name; last != customRow {
		t.Fatalf("the custom row must come last, got %q", last)
	}

	m.selectProvider(customRow)
	if m.mode != teaCustomForm {
		t.Fatalf("the form did not open, mode = %v", m.mode)
	}

	fill := func(name, url, key string) {
		m.custom = customForm{values: [3]string{name, url, key}}
		m.keyInput.SetValue(key)
		m.submitCustom()
	}

	// Each field is checked, and a bad one puts the cursor back on itself
	// rather than throwing the other two away.
	for _, bad := range []struct {
		name, url, key string
		want           int
	}{
		{"my gw", "https://x.id", "k", customName},                 // a space
		{"ACME/x", "https://x.id", "k", customName},               // a slash
		{"anthropic", "https://x.id", "k", customName},             // built in
		{strings.Repeat("a", 21), "https://x.id", "k", customName}, // too long to be a name
		// The boxes are next to each other and one of them is masked, so this
		// is the mistake that happens — and it used to write the key into
		// settings.json, which is 0644 precisely because it holds no secrets.
		{"sk-0d51811bf25", "https://x.id", "sk-0d51811bf25", customName},
		{"gw", "x.id", "k", customURL},        // no scheme
		{"gw", "https://x.id", "", customKey}, // no key
	} {
		fill(bad.name, bad.url, bad.key)
		if m.custom.focused != bad.want {
			t.Errorf("%+v: cursor went to %d, want %d", bad, m.custom.focused, bad.want)
		}
		if config.Configured("gw") {
			t.Fatalf("%+v: a rejected form saved anyway", bad)
		}
	}

	fill("acme", "https://acme.example.com/v1/", "kunci-lokal")
	if got := config.BaseURLOf("acme"); got != "https://acme.example.com/v1" {
		t.Errorf("endpoint = %q, want it saved without the trailing slash", got)
	}
	if got := config.APIKey("acme"); got != "kunci-lokal" {
		t.Errorf("key = %q, want it saved under the provider's own name", got)
	}

	// It is a provider now: findable, listed, and priced by nobody.
	if !config.Configured("acme") {
		t.Error("a saved endpoint must count as configured")
	}
	if config.Known("acme") {
		t.Error("a gateway must not become Known — that is what lets the table quote a price")
	}
	if !slices.Contains(config.Providers(), "acme") {
		t.Error("a custom provider is missing from the picker's list")
	}
	if got := config.ModelSummary("acme/sonnet-4.5"); strings.Contains(got, "$") {
		t.Errorf("a gateway priced itself: %q", got)
	}
	// And it can be built to be asked what it serves, which is what the model
	// picker does — it used to skip anything with no default model.
	if config.ListerModel("acme") == "" {
		t.Error("no model name to list with")
	}
}

// Tab parks what is typed and opens the next box, wrapping, so one key reaches
// all three.
func TestCustomFormTabKeepsWhatWasTyped(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider(customRow)

	m.keyInput.SetValue("acme")
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.custom.focused != customURL {
		t.Fatalf("tab went to %d", m.custom.focused)
	}
	if m.custom.values[customName] != "acme" {
		t.Errorf("the name was lost: %q", m.custom.values[customName])
	}
	if m.keyInput.Value() != "" {
		t.Errorf("the next box came up holding %q", m.keyInput.Value())
	}

	// Back round to the name, which is still there.
	m.updatePicker(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.custom.focused != customName || m.keyInput.Value() != "acme" {
		t.Errorf("shift+tab landed on %d holding %q", m.custom.focused, m.keyInput.Value())
	}
}

// Forgetting only the key would leave the endpoint behind, and /model would go
// on offering a provider that cannot answer.
func TestDisconnectRemovesACustomEndpointWhole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"UHAI_API_KEY", "UHAI_MODEL", "UHAI_BASE_URL"} {
		t.Setenv(env, "")
	}
	if err := config.SaveBaseURL("acme", "https://acme.example.com/v1"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save("acme", config.Creds{config.FieldKey: "k"}); err != nil {
		t.Fatal(err)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.disconnect("acme")

	if config.Configured("acme") {
		t.Error("the endpoint survived the disconnect")
	}
	if slices.Contains(config.Providers(), "acme") {
		t.Error("a disconnected gateway is still offered")
	}
}

// Paste is not a key press. It used to miss the picker entirely and land in
// the chat prompt behind it, which is where a pasted API key quietly went.
func TestPasteReachesTheBoxInFront(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.selectProvider(customRow)

	m.Update(tea.PasteMsg{Content: "sk-rahasia"})
	if got := m.keyInput.Value(); got != "sk-rahasia" {
		t.Errorf("the entry box got %q", got)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("the chat prompt behind it got %q — that is the bug", got)
	}
}

// The same rule, and the reason it is a rule rather than a courtesy to paste:
// bubbletea's filter is asynchronous. Typing into it returns a command that
// works out the matches and sends a FilterMatchesMsg back, and that message is
// not a KeyMsg — so an open picker has to receive everything, not only keys,
// or the filter box fills with text and nothing is ever filtered.
//
// Checked with a paste because that is the non-key message this package can
// construct; FilterMatchesMsg carries an unexported type. The routing they
// travel is the same line.
func TestAnOpenPickerGetsMessagesThatAreNotKeys(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.picker = m.newPicker([]list.Item{teaItem{title: "gw/a"}}, "Choose model")
	m.mode = teaModelPicker

	m.Update(tea.PasteMsg{Content: "sonnet"})
	if got := m.input.Value(); got != "" {
		t.Errorf("it reached the prompt behind the picker: %q", got)
	}
}

// A provider's models are one block. They used to be two: the best six at the
// top and the remainder after every other provider's six, which put fourteen
// of a gateway's twenty models below three providers' worth of rows and read
// as "they are missing".
func TestModelPickerKeepsAProviderTogether(t *testing.T) {
	models := []string{"a-1", "a-2", "a-3", "a-4", "a-5", "a-6", "a-7", "a-8"}
	ranked := recommended("gw", models)
	if len(ranked) != len(models) {
		t.Fatalf("ranking dropped models: %v", ranked)
	}

	var items []list.Item
	for _, m := range ranked {
		items = append(items, teaItem{title: "gw/" + m})
	}
	items = append(items, teaItem{title: "other/x"})

	// Every gw row before the first row that is not one.
	seenOther := false
	for _, it := range items {
		isGW := strings.HasPrefix(it.(teaItem).title, "gw/")
		if !isGW {
			seenOther = true
			continue
		}
		if seenOther {
			t.Fatalf("a gw model came after another provider: %v", items)
		}
	}
}

// A daemon serves several projects from one process, and the session it saved
// them into was one global. Both wrote to the same id, so the second turn
// overwrote the first project's file — root and all — and that project's
// conversation was not mixed up but gone: LatestIn found nothing for it.
func TestEachProjectKeepsItsOwnSession(t *testing.T) {
	UseStore(filestore.New(t.TempDir()))
	resetSessions(t)

	say := func(text string) *agent.Agent {
		a := agent.New(nil)
		a.History = []provider.Message{{Role: provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}}
		return a
	}
	if err := SaveSessionIn(say("halo dari A"), "/tmp/project-a"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionIn(say("halo dari B"), "/tmp/project-b"); err != nil {
		t.Fatal(err)
	}

	all, err := store.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want one session per project, got %d: %+v", len(all), all)
	}

	a, err := session.LatestIn(store, "/tmp/project-a")
	if err != nil {
		t.Fatalf("project A's conversation was lost: %v", err)
	}
	b, err := session.LatestIn(store, "/tmp/project-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Errorf("both projects share one id: %s", a.ID)
	}
	if got := firstText(a); got != "halo dari A" {
		t.Errorf("project A holds %q", got)
	}
	if got := firstText(b); got != "halo dari B" {
		t.Errorf("project B holds %q", got)
	}

	// And a second turn in A goes back into A's own file rather than forking.
	if err := SaveSessionIn(say("lagi dari A"), "/tmp/project-a"); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.All(); len(all) != 2 {
		t.Errorf("a second turn forked a session: %d on disk", len(all))
	}
}

func firstText(s session.Session) string {
	if len(s.Messages) == 0 || len(s.Messages[0].Content) == 0 {
		return ""
	}
	return s.Messages[0].Content[0].Text
}

// resetSessions clears the per-project map, which is process-wide: a test that
// leaves one behind decides what the next one sees.
func resetSessions(t *testing.T) {
	t.Helper()
	clear := func() {
		sessions.Lock()
		sessions.byRoot = map[string]session.Session{}
		sessions.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// The bill is the session's, not the process's. As a package-level slice it
// was the twin of the shared-session bug: loading project B's conversation
// replaced it wholesale, and project A's next save wrote B's cost into A's
// file.
func TestEachProjectKeepsItsOwnSpend(t *testing.T) {
	UseStore(filestore.New(t.TempDir()))
	resetSessions(t)

	recordUsageIn("/tmp/project-a", "anthropic/claude-sonnet-5",
		provider.Usage{Input: 1_000_000, Output: 100_000})
	recordUsageIn("/tmp/project-b", "anthropic/claude-sonnet-5",
		provider.Usage{Input: 2_000_000, Output: 200_000})

	a, b := spentIn("/tmp/project-a"), spentIn("/tmp/project-b")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("want one model's share each, got %v and %v", a, b)
	}
	if a[0].Usage.Input != 1_000_000 {
		t.Errorf("project A was billed %d, want 1000000 — B's turn leaked in", a[0].Usage.Input)
	}
	if b[0].Usage.Input != 2_000_000 {
		t.Errorf("project B was billed %d, want 2000000", b[0].Usage.Input)
	}

	// And it reaches disk under the right project.
	agentWith := func(text string) *agent.Agent {
		ag := agent.New(nil)
		ag.History = []provider.Message{{Role: provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}}
		return ag
	}
	if err := SaveSessionIn(agentWith("a"), "/tmp/project-a"); err != nil {
		t.Fatal(err)
	}
	saved, err := session.LatestIn(store, "/tmp/project-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Spend) != 1 || saved.Spend[0].Usage.Input != 1_000_000 {
		t.Errorf("the saved bill is %v", saved.Spend)
	}
}

// Everything in the chat starts in the same column. A note written at column 0
// beside an answer glamour indents by two reads as a different pane, which is
// what it looked like: the permission line sat left of the tool call it was
// about.
func TestChatLinesShareOneGutter(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	m.input.SetValue("halo")
	m.submit()
	m.add(chatEntry{kind: entryAnswer, text: "Sebuah jawaban."})
	m.Update(teaToolMsg(toolLine("run_bash", `{"command":"ls"}`)))
	m.addHistory(teaDim.Render("run_bash is allowed for the rest of this session"))

	for _, row := range strings.Split(m.chat.View(), "\n") {
		row = plain(row)
		if strings.TrimSpace(row) == "" || strings.HasPrefix(row, "╭") ||
			strings.HasPrefix(row, "│") || strings.HasPrefix(row, "╰") {
			continue // the welcome box is drawn to the full width on purpose
		}
		if !strings.HasPrefix(row, chatGutter) || strings.HasPrefix(row, chatGutter+" ") {
			t.Errorf("every chat row starts in the gutter, got %q", row)
		}
	}
}

// Attached, the question comes over a socket, but the answer is decided here.
// It used to skip both rules — the project's and this session's "always" — so
// a daemon asked about run_bash again every single call.
func TestSettledHonoursAlwaysAndProjectRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"deny":["Bash(git push:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if _, decided := m.settled("run_bash", `{"command":"ls"}`); decided {
		t.Fatal("nothing has been said about ls, so somebody has to be asked")
	}
	m.allowed.add("run_bash")
	if allow, decided := m.settled("run_bash", `{"command":"ls"}`); !allow || !decided {
		t.Fatal(`"always" must settle the next one without asking`)
	}
}

// A tool call with nothing to show is a name, not a name and a space. The
// daemon sent no arguments at all, and the trailing space it left was the
// visible half of that.
func TestAToolLineWithNothingToShowIsJustTheName(t *testing.T) {
	for _, input := range []string{"", "   ", "{}"} {
		if got := toolLine("run_bash", input); got != "run_bash" {
			t.Errorf("toolLine(run_bash, %q) = %q, want %q", input, got, "run_bash")
		}
	}
}

// A model whose provider has no reasoning field writes the working out into
// the answer and fences it with <think>. A gateway that does not map that to
// reasoning_content passes the tags through, and they were drawn literally.
func TestAFinishedThinkBlockDisappears(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	out := plain(m.rendererText("<think>\nBaca diff dulu.\n</think>\n\nDua endpoint baru."))
	if strings.Contains(out, "<think>") || strings.Contains(out, "</think>") {
		t.Fatalf("the tags must not be drawn as themselves: %q", out)
	}
	// Closed means the answer is here, so the wait leaves nothing behind — no
	// working out, and no marker where it was.
	if strings.Contains(out, "Baca diff dulu.") || strings.Contains(out, "✻") {
		t.Fatalf("a closed block leaves nothing behind:\n%s", out)
	}
	if strings.TrimSpace(plain(out)) != "Dua endpoint baru." {
		t.Fatalf("only the answer is left, on its own: %q", out)
	}
}

// The markers are padded for glamour, and the live block does not run
// glamour: the padding was drawn as two empty rows each side.
func TestTheLiveBlockDoesNotDrawTheMarkerPadding(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.stream = "<think>\nBaca diff dulu.\n</think>\n\nDua endpoint baru."

	var rows []string
	for _, row := range m.streamRows() {
		rows = append(rows, strings.TrimSpace(plain(row)))
	}
	want := []string{"Dua endpoint baru."}
	if !slices.Equal(rows, want) {
		t.Fatalf("streamRows() = %q, want %q", rows, want)
	}
}

// While it is still arriving there is no answer to bury, so the working out
// stays and says what it is.
func TestAnUnclosedThinkBlockKeepsItsWorkingOut(t *testing.T) {
	m := newTeaModel(agent.New(nil), nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	out := plain(m.rendererText("<think>\nBaca diff du"))
	if !strings.Contains(out, "✻ thinking") || !strings.Contains(out, "Baca diff du") {
		t.Fatalf("an open block keeps its marker and its text:\n%s", out)
	}
}
