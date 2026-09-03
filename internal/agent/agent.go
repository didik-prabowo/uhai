// Package agent holds the main agentic loop: send messages to the provider,
// run any tool it asks for, send the result back, repeat. It depends only on
// the provider.Provider interface, never on a specific vendor.
package agent

import (
	"context"
	"fmt"

	"github.com/didik-prabowo/ouhai/internal/provider"
	"github.com/didik-prabowo/ouhai/internal/task"
	"github.com/didik-prabowo/ouhai/internal/tools"
)

const DefaultSystemPrompt = `You are ouhai, a CLI coding agent running in the user's terminal.
You help with software engineering tasks.

# Harness
- Your output is printed straight into the terminal as plain text.
- Tools: read_file (read a file), edit_file (replace an exact piece of text in
  a file), write_file (write/overwrite a whole file, parent folders are
  created), glob (find files by name), grep (search file contents),
  run_bash (run one shell command, returns stdout+stderr).
- Read a file before changing it. To change part of a file use edit_file; keep
  write_file for new files and full rewrites.
- Search with glob and grep, not run_bash: they need no permission, so they do
  not interrupt the user. Keep run_bash for verifying (build, test) and for
  anything the other tools cannot do. Refer to locations as path/to/file.go:12
  so they are clickable.
- Call independent tools together in one turn when you can.
- spawn_task hands a self-contained job to a fresh agent and returns only its
  report. Use it when answering needs a lot of reading (searching the codebase,
  investigating a bug) so the findings come back without the file dumps. Keep
  work you need to see the details of for yourself.

# How to work
- Do what was asked, no more and no less. Never quietly widen or narrow scope.
- Mild ambiguity: decide like a careful colleague would and state the
  assumption. Ask only when two readings lead to very different work.
- Prefer editing an existing file over creating a new one. Do not write READMEs
  or docs unless asked.
- Match the style already in the repo (naming, comments, idioms).
- Verify your work when there is a way to (build/test), then report honestly.
  If tests fail, say so with the output. If you skipped something, say that.
- Hard-to-reverse actions (deleting files, git push, sending data somewhere)
  need confirmation first unless clearly instructed.

# Output style
- Be brief. Answer the point, no preamble or wrap-up.
- Say in one line what you are about to do before calling a tool.
- Do not paste long file contents back; cite path and line instead.
- When the task is done, give a short summary and stop calling tools.`

const defaultMaxIterations = 25

// defaultMaxContextTokens is when the history gets summarized, for a caller
// that names no limit. Well under the smallest context window in common use,
// so there is room for the answer; whoever knows the model sets the real one.
const defaultMaxContextTokens = 32000

// compactKeepMessages is how much of the tail survives compaction verbatim.
const compactKeepMessages = 4

const compactPrompt = `You are summarizing a coding session so it can replace the
conversation history, which has grown too long.

Write notes to your future self, not a report to the user. Keep: what the user
asked for, decisions taken and why, files read or written and what is in them,
commands run and their result, and anything still unfinished. Drop pleasantries
and any file content that is no longer relevant. Be dense, no preamble.`

// Asker is the narrowest useful description of an agent: something a prompt
// can be put to. It is declared for the callers that will one day hold work
// without knowing what kind it is — an agent behind an RPC, or one that is not
// a model at all.
//
// Nothing uses it yet, and that is said out loud rather than hidden: the seam
// this repository actually runs on is task.Runner, a function, which is what
// let /check join /bg as a second kind of work without a new type. Widen this
// only when a caller needs more than Ask, and expect to need a way to build an
// agent as well — configuring one takes six fields, and an interface hands
// over none of them.
type Asker interface {
	Ask(ctx context.Context, prompt string) error
}

// The agent satisfies it as written; the assertion is here so that stays true.
var _ Asker = (*Agent)(nil)

// Agent wraps one Provider plus conversation history and observation hooks.
type Agent struct {
	Provider      provider.Provider
	System        string
	MaxIterations int
	History       []provider.Message

	// OnText fires for every text block from the model, complete. OnDelta
	// fires for each piece of that same text while it streams in, so a caller
	// can show progress; leaving it nil turns streaming off. OnToolCall fires
	// just before a tool runs.
	OnText     func(text string)
	OnDelta    func(delta string)
	OnToolCall func(name string, input string)

	// OnNotice reports something the agent did on its own, such as compacting
	// the history.
	OnNotice func(text string)

	// LastUsage is what the last provider call cost, when the provider says.
	LastUsage provider.Usage

	// OnUsage fires after every provider call with what that call cost, so a
	// live UI can show tokens while the turn is still running.
	OnUsage func(provider.Usage)

	// Tasks records every job spawned through spawn_task.
	Tasks *task.Registry

	// nested marks an agent that is itself a task, so it is not offered
	// spawn_task in turn.
	nested bool

	// MaxContextTokens triggers compaction once the history grows past it.
	// Zero turns automatic compaction off.
	MaxContextTokens int

	// AllowTool decides whether one tool may be offered and run at all. Nil
	// offers everything. It is a function rather than a list because the
	// answer lives in settings, which this package must not know about — and
	// because a project may change its mind between turns.
	AllowTool func(name string) bool

	// UseTools is whether the model is offered any. A model that cannot take
	// them answers an error to every turn otherwise, which reads as the agent
	// being broken rather than the model being the wrong one.
	UseTools bool

	// Confirm is asked for tools that need permission (tools.NeedsConfirm).
	// Returning false skips the tool and tells the model it was denied. The
	// default denies everything, so a caller that forgets this hook can never
	// silently write files or run commands.
	Confirm func(name string, input string) bool
}

func New(p provider.Provider) *Agent {
	return &Agent{
		Provider:         p,
		System:           DefaultSystemPrompt,
		MaxIterations:    defaultMaxIterations,
		MaxContextTokens: defaultMaxContextTokens,
		UseTools:         true,
		Tasks:            &task.Registry{},
		OnText:           func(string) {},
		OnToolCall:       func(string, string) {},
		OnNotice:         func(string) {},
		Confirm:          func(string, string) bool { return false },
	}
}

func (a *Agent) systemPrompt() string {
	if a.Provider == nil {
		return a.System
	}
	return a.System + fmt.Sprintf(`

# Runtime identity
- The current provider/model is %s.
- If asked which model or provider you use, answer with that exact provider/model. Do not claim GPT-4 or another model unless it is the current identity above.`, a.Provider.Name())
}

// Ask appends the user prompt to the history, then runs the agentic loop
// until the model is done, the iteration limit is hit, or ctx is cancelled.
func (a *Agent) Ask(ctx context.Context, userPrompt string) error {
	a.History = append(a.History, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: userPrompt}},
	})

	for i := 0; i < a.MaxIterations; i++ {
		if a.MaxContextTokens > 0 && a.Tokens() > a.MaxContextTokens {
			before := a.Tokens()
			if err := a.Compact(ctx); err != nil {
				a.OnNotice(fmt.Sprintf("could not compact the history: %v", err))
			} else {
				a.OnNotice(fmt.Sprintf("history compacted: %d → %d tokens", before, a.Tokens()))
			}
		}

		resp, err := a.Provider.Send(ctx, provider.Request{
			System:   a.systemPrompt(),
			Messages: a.History,
			Tools:    a.tools(),
			Stream:   a.OnDelta,
		})
		if err != nil {
			// No wrapping: every client already names itself and the status
			// it got, and "provider error: provider error (HTTP 400)" reads
			// like the program stuttered.
			return err
		}
		a.LastUsage = resp.Usage
		if a.OnUsage != nil {
			a.OnUsage(resp.Usage)
		}

		for _, block := range resp.Content {
			if block.Type == provider.BlockText && block.Text != "" {
				a.OnText(block.Text)
			}
		}

		a.History = append(a.History, provider.Message{
			Role:    provider.RoleAssistant,
			Content: resp.Content,
		})

		if resp.StopReason != provider.StopToolUse {
			return nil
		}

		toolResults := a.runTools(ctx, resp.Content)
		a.History = append(a.History, provider.Message{
			Role:    provider.RoleUser,
			Content: toolResults,
		})
	}

	return fmt.Errorf("stopped: hit the %d iteration limit without finishing", a.MaxIterations)
}

func (a *Agent) runTools(ctx context.Context, blocks []provider.ContentBlock) []provider.ContentBlock {
	var results []provider.ContentBlock
	for _, block := range blocks {
		if block.Type != provider.BlockToolUse {
			continue
		}

		result, isError := "", false
		switch {
		case a.AllowTool != nil && !a.AllowTool(block.ToolName):
			// Denied, and called anyway — some models will try what they
			// remember from another conversation.
			result, isError = "this tool is switched off in this project", true
		case tools.NeedsConfirm(block.ToolName) && !a.Confirm(block.ToolName, string(block.ToolInput)):
			result, isError = "the user denied running this tool", true
		case block.ToolName == spawnSpec.Name:
			// Spawning is handled here, not in the tools package: a task needs
			// an agent, and the tools cannot import the thing that uses them.
			a.OnToolCall(block.ToolName, string(block.ToolInput))
			result, isError = a.spawn(ctx, block.ToolInput)
		default:
			a.OnToolCall(block.ToolName, string(block.ToolInput))
			result, isError = tools.Execute(ctx, block.ToolName, block.ToolInput)
		}

		results = append(results, provider.ContentBlock{
			Type:            provider.BlockToolResult,
			ToolResultForID: block.ToolUseID,
			ToolResultText:  result,
			ToolResultError: isError,
		})
	}
	return results
}

// tools is what the model may call: the ordinary tools, plus spawning a task
// unless this agent is a task itself.
func (a *Agent) tools() []provider.ToolSpec {
	if !a.UseTools {
		return nil
	}

	var specs []provider.ToolSpec
	for _, spec := range tools.Definitions() {
		// A tool that is denied is not offered: a model cannot misuse what it
		// was never told about, and refusing after the fact wastes a turn.
		if a.AllowTool != nil && !a.AllowTool(spec.Name) {
			continue
		}
		specs = append(specs, spec)
	}
	if a.nested {
		return specs
	}
	return append(specs, spawnSpec)
}

// Tokens estimates how many tokens the history occupies. Four characters per
// token is the usual rule of thumb for English and for code; it is only used
// to decide when to compact, so being a little off costs nothing.
func (a *Agent) Tokens() int {
	chars := len(a.System)
	for _, m := range a.History {
		for _, b := range m.Content {
			chars += len(b.Text) + len(b.ToolInput) + len(b.ToolResultText)
		}
	}
	return chars / 4
}

// Compact replaces the history with a summary of it, keeping the last few
// messages verbatim, so a long session can continue instead of dying at the
// model's context limit.
func (a *Agent) Compact(ctx context.Context) error {
	if len(a.History) < 2 {
		return nil
	}

	cut := cutPoint(a.History, compactKeepMessages)
	older := a.History[:cut:cut]
	if len(older) == 0 {
		return nil
	}

	resp, err := a.Provider.Send(ctx, provider.Request{
		System: compactPrompt,
		Messages: append(older, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "Write the summary now."}},
		}),
	})
	if err != nil {
		return err
	}

	var summary string
	for _, b := range resp.Content {
		if b.Type == provider.BlockText {
			summary += b.Text
		}
	}
	if summary == "" {
		return fmt.Errorf("the summary came back empty")
	}

	a.History = append([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "Summary of the session so far:\n\n" + summary}},
	}}, a.History[cut:]...)
	a.LastUsage = provider.Usage{}
	return nil
}

// cutPoint picks where the tail kept verbatim starts: the newest message that
// opens a clean turn, no more than keep messages from the end. A tool result
// must never be separated from the call it answers — providers reject that —
// so the tail cannot begin just anywhere.
func cutPoint(h []provider.Message, keep int) int {
	for i := len(h) - keep; i > 0; i-- {
		if h[i].Role == provider.RoleUser && len(h[i].Content) > 0 && h[i].Content[0].Type == provider.BlockText {
			return i
		}
	}
	return len(h) // no clean boundary: summarize the lot
}
