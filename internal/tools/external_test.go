// The proof that Tool is an interface and not a shape: a tool defined outside
// the package, registered at run time, and reached through Execute like any
// other. Nothing built in can demonstrate that — a built-in would pass just as
// well if Tool were a struct.
package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/didik-prabowo/uhai/internal/tools"
)

// jiraTool stands in for the reason the interface exists: a tool whose name is
// not known when this package is compiled — from an MCP server, or a plugin.
type jiraTool struct{ called string }

func (j *jiraTool) Name() string            { return "search_jira" }
func (j *jiraTool) Description() string     { return "Search issues." }
func (j *jiraTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (j *jiraTool) NeedsConfirm() bool      { return true }

func (j *jiraTool) Run(_ context.Context, _ string, input json.RawMessage) (string, bool) {
	j.called = string(input)
	return "PROJ-1, PROJ-2", false
}

func TestAToolCanComeFromAnotherPackage(t *testing.T) {
	jira := &jiraTool{}
	if err := tools.Register(jira); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.Unregister("search_jira") })

	// It reaches the model like any other.
	var offered bool
	for _, spec := range tools.Definitions() {
		if spec.Name == "search_jira" && spec.Description == "Search issues." {
			offered = true
		}
	}
	if !offered {
		t.Error("a registered tool must be offered to the model")
	}

	// It obeys the same permission rule.
	if !tools.NeedsConfirm("search_jira") {
		t.Error("a registered tool must be able to ask first")
	}

	// And it runs.
	out, isErr := tools.Execute(context.Background(), "", "search_jira", json.RawMessage(`{"q":"bug"}`))
	if isErr || !strings.Contains(out, "PROJ-1") {
		t.Fatalf("the registered tool did not run: %q %v", out, isErr)
	}
	if jira.called != `{"q":"bug"}` {
		t.Errorf("the input must reach it unchanged, got %q", jira.called)
	}

	// Two tools answering to one name is a bug with nothing to read, so the
	// second one is refused rather than shadowing the first.
	if err := tools.Register(&jiraTool{}); err == nil {
		t.Error("a duplicate name must be refused")
	}
}

// A registry that is only safe when used the way its author imagined is not
// safe. An MCP server reconnecting mid-session would register from its own
// goroutine while the agent reads the list from its — this is that, and it
// fails under -race without the lock.
func TestRegisterIsSafeWhileTheListIsRead(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("late_%d", i)
			if err := tools.Register(namedTool{name}); err != nil {
				t.Error(err)
			}
			t.Cleanup(func() { tools.Unregister(name) })
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tools.Definitions()
			tools.NeedsConfirm("read_file")
			tools.Execute(context.Background(), "", "glob", json.RawMessage(`{}`))
		}()
	}
	wg.Wait()
}

type namedTool struct{ n string }

func (t namedTool) Name() string            { return t.n }
func (t namedTool) Description() string     { return "late arrival" }
func (t namedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t namedTool) NeedsConfirm() bool      { return false }

func (t namedTool) Run(context.Context, string, json.RawMessage) (string, bool) { return "ok", false }
