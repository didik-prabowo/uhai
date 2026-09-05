package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/task"
	"github.com/didik-prabowo/uhai/internal/tools"
)

// spawnSpec is offered to the model on top of the ordinary tools, but never to
// a task itself — a subagent that can spawn subagents is a fork bomb with a
// billing account.
var spawnSpec = provider.ToolSpec{
	Name: tools.NameSpawnTask,
	Description: "Hand a self-contained piece of work to a fresh agent with its own context, and get back only its report. " +
		"Use it for work that reads a lot to answer a little: searching the codebase, investigating a bug, reviewing a diff. " +
		"The task cannot ask the user anything and shares no history with you, so its prompt must be complete on its own.",
	JSONSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"description": {"type": "string", "description": "3-5 words naming the task, shown to the user"},
			"prompt": {"type": "string", "description": "The full instruction for the task, including what to report back"}
		},
		"required": ["description", "prompt"]
	}`),
}

const subagentPrompt = `

# You are a task
You were spawned by the main agent to do one job and report back.
- You share no history with it, and you cannot ask the user anything: work with
  what the prompt gives you, and say so plainly if it is not enough.
- Your final message is the whole report. Nothing else you do is visible to the
  agent that spawned you, so put the findings, the file paths and the line
  numbers in it — not "see above".
- Report in full but without padding: no preamble, no offer to help further.`

// spawn runs one task to completion and returns its report as the tool result.
func (a *Agent) spawn(ctx context.Context, input json.RawMessage) (string, bool) {
	var args struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "prompt must not be empty", true
	}

	// RunNow, not Run: the model is blocked on this call and so is the user,
	// so it must not queue behind whatever /bg started.
	t, report, err := a.Tasks.RunNow(ctx, args.Description, func(ctx context.Context, t task.Task) (string, int, error) {
		a.OnNotice(fmt.Sprintf("%s %s — running", t.ID, t.Description))

		sub := New(a.Provider)
		sub.System = a.System + subagentPrompt
		sub.MaxIterations = a.MaxIterations
		sub.MaxContextTokens = a.MaxContextTokens
		sub.nested = true
		// Permission stays with the user: a task asks through the same prompt
		// the main agent does, rather than being trusted because it is nested.
		sub.Confirm = a.Confirm
		// And the denials come with it. Without this a spawned agent was
		// handed the tools the settings had refused — Confirm would still have
		// asked before a write, but "deny" means never, not "ask again in a
		// different window".
		sub.AllowTool = a.AllowTool
		if a.onSpawn != nil {
			a.onSpawn(sub)
		}
		sub.OnToolCall = func(name, input string) { a.OnToolCall(t.ID+" "+name, input) }
		sub.OnNotice = a.OnNotice

		// Only the last thing a task says is its report; the rest is thinking
		// out loud between tool calls.
		var report string
		sub.OnText = func(text string) { report = text }

		// The answer as it is written, so the task can be looked in on with
		// /tasks while it runs — and so a task that dies halfway leaves the
		// part it managed to write.
		sub.OnDelta = func(delta string) { a.Tasks.Progress(t.ID, delta) }

		err := sub.Ask(ctx, args.Prompt)
		return report, sub.Tokens(), err
	})

	a.OnNotice(fmt.Sprintf("%s %s — %s in %s, ~%d tokens",
		t.ID, t.Description, t.Status, t.Elapsed.Round(time.Second), t.Tokens))

	if err != nil {
		failed := fmt.Sprintf("task %s failed: %v", t.ID, err)
		// A task that ran for five minutes and died on a rate limit still
		// found things out. Half an answer is worth more to the model than
		// the news that there is none.
		if partial := strings.TrimSpace(t.Output); partial != "" {
			failed += "\n\nWhat it had written before it failed:\n" + partial
		}
		return failed, true
	}
	if strings.TrimSpace(report) == "" {
		return fmt.Sprintf("task %s finished without reporting anything", t.ID), true
	}
	return report, false
}
