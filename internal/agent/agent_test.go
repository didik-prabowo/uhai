package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/task"
)

// fakeProvider asks for write_file on the first call, then finishes.
type fakeProvider struct {
	path  string
	calls int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Send(_ context.Context, req provider.Request) (*provider.Response, error) {
	f.calls++
	if f.calls == 1 {
		input, _ := json.Marshal(map[string]string{"path": f.path, "content": "halo"})
		return &provider.Response{
			StopReason: provider.StopToolUse,
			Content: []provider.ContentBlock{{
				Type:      provider.BlockToolUse,
				ToolUseID: "t1",
				ToolName:  "write_file",
				ToolInput: input,
			}},
		}, nil
	}
	return &provider.Response{
		StopReason: provider.StopEndTurn,
		Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "oke"}},
	}, nil
}

func TestConfirmDeniedSkipsTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	a := New(&fakeProvider{path: path})
	a.Confirm = func(string, string) bool { return false }

	if err := a.Ask(context.Background(), "write a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file created even though the user denied it: %v", err)
	}

	last := a.History[len(a.History)-2].Content[0]
	if !last.ToolResultError || !strings.Contains(last.ToolResultText, "denied") {
		t.Fatalf("model was not told about the denial: %+v", last)
	}
}

func TestConfirmDefaultDenies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	a := New(&fakeProvider{path: path}) // New()'s default Confirm, not overridden
	if err := a.Ask(context.Background(), "write a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the default Confirm should deny, but the file was created")
	}
}

func TestConfirmAllowedRunsTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	a := New(&fakeProvider{path: path})
	a.Confirm = func(string, string) bool { return true }

	if err := a.Ask(context.Background(), "write a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file should have been created: %v", err)
	}
}

// summarizer answers every call with a fixed summary, standing in for the
// model during compaction.
type summarizer struct{ got []provider.Message }

func (s *summarizer) Name() string { return "fake" }

func (s *summarizer) Send(_ context.Context, req provider.Request) (*provider.Response, error) {
	s.got = req.Messages
	return &provider.Response{
		StopReason: provider.StopEndTurn,
		Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "the session so far"}},
	}, nil
}

func text(role provider.Role, s string) provider.Message {
	return provider.Message{Role: role, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: s}}}
}

func TestCompactKeepsToolPairsIntact(t *testing.T) {
	p := &summarizer{}
	a := New(p)
	a.History = []provider.Message{
		text(provider.RoleUser, "one"),
		text(provider.RoleAssistant, "answer one"),
		text(provider.RoleUser, "two"),
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockToolUse, ToolUseID: "t1", ToolName: "read_file", ToolInput: json.RawMessage(`{}`)},
		}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockToolResult, ToolResultForID: "t1", ToolResultText: "contents"},
		}},
		text(provider.RoleAssistant, "answer two"),
	}

	if err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := a.History[0].Content[0].Text; !strings.Contains(got, "the session so far") {
		t.Fatalf("the history should open with the summary, got %q", got)
	}
	// Every tool result kept must still have its call: providers reject a
	// result whose call was summarized away.
	calls := map[string]bool{}
	for _, m := range a.History[1:] {
		for _, b := range m.Content {
			if b.Type == provider.BlockToolUse {
				calls[b.ToolUseID] = true
			}
			if b.Type == provider.BlockToolResult && !calls[b.ToolResultForID] {
				t.Fatalf("tool result %q was cut loose from its call: %+v", b.ToolResultForID, a.History)
			}
		}
	}
	if len(a.History) >= 6 {
		t.Fatalf("compaction did not shorten anything: %d messages", len(a.History))
	}
	// Only the summarized part is sent to the model, plus the closing ask.
	if len(p.got) > 4 {
		t.Fatalf("the whole history was sent to be summarized: %+v", p.got)
	}
}

func TestTokensGrowsWithHistory(t *testing.T) {
	a := New(&summarizer{})
	before := a.Tokens()
	a.History = append(a.History, text(provider.RoleUser, strings.Repeat("x", 4000)))
	if got := a.Tokens() - before; got < 900 || got > 1100 {
		t.Fatalf("4000 characters should be roughly 1000 tokens, got %d", got)
	}
}

// spawner asks for a task on its first call, plays the task on the second, and
// wraps up on the third.
type spawner struct {
	calls       int
	mainTools   []string
	nestedTools []string
}

func (p *spawner) Name() string { return "fake" }

func (p *spawner) Send(_ context.Context, req provider.Request) (*provider.Response, error) {
	p.calls++
	switch p.calls {
	case 1:
		for _, t := range req.Tools {
			p.mainTools = append(p.mainTools, t.Name)
		}
		input, _ := json.Marshal(map[string]string{"description": "look around", "prompt": "count the files"})
		return &provider.Response{
			StopReason: provider.StopToolUse,
			Content: []provider.ContentBlock{{
				Type: provider.BlockToolUse, ToolUseID: "s1", ToolName: "spawn_task", ToolInput: input,
			}},
		}, nil
	case 2: // this call comes from the task, not the main agent
		for _, t := range req.Tools {
			p.nestedTools = append(p.nestedTools, t.Name)
		}
		return &provider.Response{
			StopReason: provider.StopEndTurn,
			Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "there are 12 files"}},
		}, nil
	default:
		return &provider.Response{
			StopReason: provider.StopEndTurn,
			Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "ok"}},
		}, nil
	}
}

func TestSpawnTaskReportsBackAndIsTracked(t *testing.T) {
	p := &spawner{}
	a := New(p)
	if err := a.Ask(context.Background(), "how many files are there?"); err != nil {
		t.Fatal(err)
	}

	// Only the task's report reaches the main history, not what it read.
	result := a.History[2].Content[0]
	if result.Type != provider.BlockToolResult || result.ToolResultError {
		t.Fatalf("the task result did not come back as a tool result: %+v", result)
	}
	if result.ToolResultText != "there are 12 files" {
		t.Fatalf("the report was not passed through: %q", result.ToolResultText)
	}

	tasks := a.Tasks.Snapshot()
	if len(tasks) != 1 || tasks[0].ID != "t1" || tasks[0].Status != task.StatusDone {
		t.Fatalf("the task was not tracked: %+v", tasks)
	}
	if tasks[0].Description != "look around" {
		t.Fatalf("description was dropped: %+v", tasks[0])
	}

	// The main agent is offered spawning; a task is not.
	var mainCanSpawn bool
	for _, name := range p.mainTools {
		mainCanSpawn = mainCanSpawn || name == "spawn_task"
	}
	if !mainCanSpawn {
		t.Fatalf("the main agent was never offered spawn_task: %v", p.mainTools)
	}

	// A task must not be able to spawn tasks of its own.
	var sawSpawn, sawRead bool
	for _, name := range p.nestedTools {
		sawSpawn = sawSpawn || name == "spawn_task"
		sawRead = sawRead || name == "read_file"
	}
	if sawSpawn {
		t.Fatalf("a task was offered spawn_task: %v", p.nestedTools)
	}
	if !sawRead {
		t.Fatalf("a task got no ordinary tools: %v", p.nestedTools)
	}
}

// A model that cannot take tools must be given none. Sending them anyway
// fails every turn, which reads as the agent being broken rather than as the
// model being the wrong one for the job.
func TestToolsFollowTheModel(t *testing.T) {
	a := New(nil)
	if len(a.tools()) == 0 {
		t.Fatal("tools are offered by default")
	}

	a.UseTools = false
	if got := a.tools(); got != nil {
		t.Fatalf("a model without tool support must be offered none, got %d", len(got))
	}
}

// A denied tool is not offered at all — a model cannot misuse what it was
// never told about — and is refused if it tries from memory anyway.
func TestDeniedToolsAreNotOffered(t *testing.T) {
	a := New(nil)
	a.AllowTool = func(name string) bool { return name != "write_file" }

	for _, spec := range a.tools() {
		if spec.Name == "write_file" {
			t.Fatal("a denied tool must not reach the model")
		}
	}
	if len(a.tools()) == 0 {
		t.Fatal("denying one tool must not remove the rest")
	}

	results := a.runTools(context.Background(), []provider.ContentBlock{{
		Type:      provider.BlockToolUse,
		ToolUseID: "tu_1",
		ToolName:  "write_file",
		ToolInput: []byte(`{"path":"/tmp/x","content":"y"}`),
	}})
	if len(results) != 1 || !results[0].ToolResultError {
		t.Fatalf("calling a denied tool must fail: %+v", results)
	}
	if !strings.Contains(results[0].ToolResultText, "switched off") {
		t.Fatalf("and say why: %q", results[0].ToolResultText)
	}
}

// dyingProvider writes a little, then fails the way a busy free tier does.
type dyingProvider struct{}

func (dyingProvider) Name() string { return "dying" }

func (dyingProvider) Send(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.Stream != nil {
		req.Stream("found three problems in tea.go")
	}
	return nil, errors.New("provider error (HTTP 429): the service may be temporarily overloaded")
}

// A task that dies halfway still found things out. Handing the model only the
// error throws away five minutes of work it asked for and paid for.
func TestSpawnReturnsWhatAFailedTaskWrote(t *testing.T) {
	a := New(dyingProvider{})
	a.OnNotice = func(string) {}
	a.OnToolCall = func(string, string) {}

	input, _ := json.Marshal(map[string]string{"description": "review the diff", "prompt": "review it"})
	out, isErr := a.spawn(context.Background(), input)

	if !isErr {
		t.Fatal("a failed task is a failed tool call")
	}
	if !strings.Contains(out, "429") {
		t.Errorf("the reason must survive: %q", out)
	}
	if !strings.Contains(out, "found three problems in tea.go") {
		t.Errorf("what it wrote before dying must survive: %q", out)
	}
}

// flakyProvider fails the first turn the way a rate limit does, then works.
type flakyProvider struct{ calls int }

func (flakyProvider) Name() string { return "flaky" }

func (f *flakyProvider) Send(_ context.Context, req provider.Request) (*provider.Response, error) {
	f.calls++
	if f.calls == 1 {
		if req.Stream != nil {
			req.Stream("half an answer")
		}
		return nil, errors.New("provider error (HTTP 429): overloaded")
	}
	return &provider.Response{
		StopReason: provider.StopEndTurn,
		Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "oke"}},
	}, nil
}

// A turn that fails leaves the history ending on the user's prompt, and the
// Messages API refuses two user messages in a row — so the next prompt was
// refused for a reason belonging to the one before it.
func TestFailedTurnLeavesTheHistoryUsable(t *testing.T) {
	a := New(&flakyProvider{})
	a.OnNotice = func(string) {}

	if err := a.Ask(context.Background(), "pertama"); err == nil {
		t.Fatal("the first turn was supposed to fail")
	}
	if err := a.Ask(context.Background(), "kedua"); err != nil {
		t.Fatalf("the turn after a failure must work: %v", err)
	}

	for i := 1; i < len(a.History); i++ {
		if a.History[i].Role == a.History[i-1].Role {
			t.Fatalf("two %s messages in a row at %d: %+v", a.History[i].Role, i, a.History)
		}
	}
	// What was streamed before the failure is what the screen showed, so the
	// history has to agree with it.
	var closed string
	for _, m := range a.History {
		for _, b := range m.Content {
			if m.Role == provider.RoleAssistant && strings.Contains(b.Text, "half an answer") {
				closed = b.Text
			}
		}
	}
	if closed == "" {
		t.Fatal("the partial answer must be kept in the history")
	}
	if !strings.Contains(closed, "429") {
		t.Errorf("the closing line should say why it stopped: %q", closed)
	}
}
