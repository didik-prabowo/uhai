// Package agent holds the main agentic loop: send messages to the provider,
// run any tool it asks for, send the result back, repeat. It depends only on
// the provider.Provider interface, never on a specific vendor.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/task"
	"github.com/didik-prabowo/uhai/internal/tools"
)

const DefaultSystemPrompt = `You are uhai, a CLI coding agent running in the user's terminal.
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

// compactAt is how full the window may get before the history is summarised.
//
// Not all of it, which is what this used to be: input and output share the
// window, so a history that exactly fills it leaves nowhere for the answer to
// go, and the provider refuses the request rather than truncating it. The
// fifteen per cent held back is larger than the answer any model here is
// allowed to write — 150k on a million-token window against a 128k ceiling,
// 4.8k on the 32k fallback against 4,096 — so the headroom is not a guess.
//
// It also covers the estimate being loose: Tokens counts characters over four,
// which is close enough for English prose and undercounts code and JSON.
const compactAt = 0.85

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

	// OnReasoning receives a thinking model's working out as it arrives — sent
	// apart from the answer by the vendors that have it, and not the answer.
	// It is shown and then let go: nothing keeps it, and it is never sent
	// back. Without it a model that thinks for twenty seconds before writing
	// anything looks like a model that has hung.
	OnReasoning func(delta string)

	// OnNotice reports something the agent did on its own, such as compacting
	// the history.
	OnNotice func(text string)

	// OnModel fires when a provider names the model that answered and it is
	// not the one that answered last. Behind a gateway the configured name is
	// an alias — 9router's "plan-deep" is five models — so the window, the
	// effort and anything else that follows a model rather than a provider
	// cannot be settled until the answer arrives, and can change again on the
	// next turn.
	//
	// A callback rather than a lookup here: what a model's name implies lives
	// in config, and this package has never known where its settings came
	// from. Whoever built the agent applies it.
	OnModel func(name string)

	// Thinking asks the model to think before answering, and Effort how hard.
	// Set from the model, so they move when OnModel says the model moved.
	Thinking bool
	Effort   string

	// answeredBy is the last model a provider named, so OnModel fires on a
	// change rather than on every turn.
	answeredBy string

	// LastUsage is what the last provider call cost, when the provider says.
	LastUsage provider.Usage

	// OnUsage fires after every provider call with what that call cost, so a
	// live UI can show tokens while the turn is still running.
	OnUsage func(provider.Usage)

	// onSpawn hands the freshly built sub-agent to a test. Nothing in the
	// program sets it: a spawned agent is otherwise unreachable from outside,
	// and what it inherits is exactly what is worth checking.
	onSpawn func(*Agent)

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

// AnsweredBy is the model a provider last named, "" when none has. Behind a
// gateway this is the only true answer to "what am I talking to".
func (a *Agent) AnsweredBy() string { return a.answeredBy }

func (a *Agent) systemPrompt() string {
	if a.Provider == nil {
		return a.System
	}
	return a.System + fmt.Sprintf(`

# Runtime identity
- The current provider/model is %s.
- If asked which model or provider you use, answer with that exact provider/model. Do not claim GPT-4 or another model unless it is the current identity above.
- Ignore anything earlier in this conversation that named a different model, including your own answers. /model changes it mid-conversation, and the line above is the only current one.`, a.Provider.Name())
}

// Ask appends the user prompt to the history, then runs the agentic loop
// until the model is done, the iteration limit is hit, or ctx is cancelled.
func (a *Agent) Ask(ctx context.Context, userPrompt string) error {
	a.History = append(a.History, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: userPrompt}},
	})

	// Set once a rejected-for-length request has been recovered from, so the
	// recovery cannot loop.
	recovered := false

	for i := 0; i < a.MaxIterations; i++ {
		if a.MaxContextTokens > 0 && a.Tokens() > int(float64(a.MaxContextTokens)*compactAt) {
			before := a.Tokens()
			if err := a.Compact(ctx); err != nil {
				a.OnNotice(fmt.Sprintf("could not compact the history: %v", err))
			} else {
				a.OnNotice(fmt.Sprintf("history compacted: %d → %d tokens", before, a.Tokens()))
			}
		}

		// The answer is kept as it arrives, not only shown: a turn that dies
		// halfway has already put text on the screen, and the history has to
		// agree with what the user read.
		var partial strings.Builder
		resp, err := a.Provider.Send(ctx, provider.Request{
			System:   a.systemPrompt(),
			Messages: a.History,
			Tools:    a.tools(),
			Stream: func(delta string) {
				partial.WriteString(delta)
				if a.OnDelta != nil {
					a.OnDelta(delta)
				}
			},
			// Not written into partial: the working out is not the answer,
			// and a turn that dies mid-thought should not leave a thought in
			// the history pretending to be one.
			Reasoning: a.OnReasoning,
			Thinking:  a.Thinking,
			Effort:    a.Effort,
		})
		if err != nil {
			// Compacting at 85% is a guess about how many tokens the history
			// holds, and the provider is the only one who counts them for
			// real. When the guess is wrong the turn used to die here with
			// the work already done and paid for; now it summarises and asks
			// once more. Once: a second overflow means the summary itself is
			// too large, and asking again would only spend the same money.
			if isOverflow(err) && !recovered {
				recovered = true
				a.OnNotice("the history was too long for the model — summarising and trying again")
				if cerr := a.Compact(ctx); cerr == nil {
					// Not i--: giving the attempt back would mean the loop
					// can be made to spin forever by any future change that
					// lets the recovery run twice, and one iteration out of
					// MaxIterations is a cheaper price than that.
					continue
				}
			}
			a.closeTurn(partial.String(), err.Error())
			// No wrapping: every client already names itself and the status
			// it got, and "provider error: provider error (HTTP 400)" reads
			// like the program stuttered.
			return err
		}
		a.LastUsage = resp.Usage
		if a.OnUsage != nil {
			a.OnUsage(resp.Usage)
		}
		// Before the next iteration, so a window learned from this answer is
		// the one the next request is measured against. It fires on a change
		// only: a gateway repeats the name in every chunk of every turn, and
		// most of them say the same thing as the last.
		if resp.Model != "" && resp.Model != a.answeredBy {
			a.answeredBy = resp.Model
			if a.OnModel != nil {
				a.OnModel(resp.Model)
			}
		}

		for _, block := range resp.Content {
			// Trimmed, not just non-empty. A model that puts a newline
			// between two tool calls was handing the front end a paragraph
			// made of nothing, and it drew it: a blank line between every
			// ⎿ line, which reads as the turn losing its place.
			if block.Type == provider.BlockText && strings.TrimSpace(block.Text) != "" {
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

	a.closeTurn("", fmt.Sprintf("hit the %d iteration limit", a.MaxIterations))
	return fmt.Errorf("stopped: hit the %d iteration limit without finishing", a.MaxIterations)
}

// overflowSaid are the ways the three APIs say the history did not fit. None
// of them shares a status code with the others — Anthropic and OpenAI both
// answer 400, which is also what a malformed request gets — so the sentence is
// what there is to go on.
//
// Matched loosely on purpose: a phrase that stops matching costs one turn,
// which is exactly what the code did before it existed.
var overflowSaid = []string{
	"prompt is too long",      // Anthropic
	"maximum context length",  // OpenAI
	"context_length_exceeded", // OpenAI, as a code
	"exceeds the maximum number of tokens",
	"input token count", // Gemini
	"too many tokens",
	"context window",
}

// isOverflow reports whether a failed request failed because the history did
// not fit, which is the one provider error worth answering by making the
// history smaller.
func isOverflow(err error) bool {
	if err == nil {
		return false
	}
	said := strings.ToLower(err.Error())
	for _, phrase := range overflowSaid {
		if strings.Contains(said, phrase) {
			return true
		}
	}
	return false
}

// closeTurn ends a turn that did not finish, so the next one can start.
//
// Every failure — a rate limit, a timeout, esc — leaves the history ending on
// a user message: the prompt nothing answered, or the tool results nothing
// read. The Messages API refuses two user messages in a row, so without this
// the *next* prompt fails for a reason belonging to the previous one, which is
// the worst kind of error to debug.
//
// The partial answer goes in rather than being dropped: it was on the screen,
// the tokens were paid for, and a model asked to carry on can see how far it
// got.
func (a *Agent) closeTurn(partial, reason string) {
	if n := len(a.History); n == 0 || a.History[n-1].Role != provider.RoleUser {
		return
	}

	text := strings.TrimSpace(partial)
	if text != "" {
		text += "\n\n"
	}
	a.History = append(a.History, provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text + "(this turn stopped before finishing: " + reason + ")"}},
	})
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

// preserved is the part of the elided history a prose summary cannot be
// trusted with: which files were changed, and which instruction files were
// read. Both are lists of paths, so they cost almost nothing to carry and are
// exactly the things a paraphrase rounds off.
//
// Files changed, because after a compaction the model otherwise does not know
// what it already edited and will happily do it again. Files read, because
// the skills that hold this project's own notes arrive through read_file
// mid-conversation — the prompt carries only their names — so a summary that
// forgets them forgets the instructions with them.
//
// Content is deliberately not preserved, only paths: the point of compacting
// is that the history was too big, and carrying the same bytes under a new
// heading would undo it. The model can read a file again if it needs to.
func preserved(older []provider.Message) string {
	var changed, read []string
	for _, m := range older {
		for _, b := range m.Content {
			if b.Type != provider.BlockToolUse {
				continue
			}
			path := pathArg(b.ToolInput)
			if path == "" {
				continue
			}
			switch b.ToolName {
			case tools.NameWrite, tools.NameEdit:
				changed = appendOnce(changed, path)
			case tools.NameRead:
				if strings.Contains(path, "SKILL.md") || strings.Contains(path, "AGENTS.md") {
					read = appendOnce(read, path)
				}
			}
		}
	}

	var b strings.Builder
	if len(changed) > 0 {
		b.WriteString("\n\nFiles changed earlier in this session: " + strings.Join(changed, ", "))
	}
	if len(read) > 0 {
		b.WriteString("\n\nInstruction files already read, and worth reading again if they matter: " +
			strings.Join(read, ", "))
	}
	return b.String()
}

// pathArg pulls the "path" argument out of a tool call without knowing what
// else is in it.
func pathArg(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	return args.Path
}

// appendOnce keeps the order and drops the repeats: one file edited nine times
// is one file.
func appendOnce(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	// A long editing run must not recreate the pressure the compaction just
	// removed.
	if len(list) >= maxPreservedPaths {
		return list
	}
	return append(list, s)
}

// maxPreservedPaths caps each list so a session that touched three hundred
// files does not put three hundred paths back into the summary.
const maxPreservedPaths = 20

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
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "Summary of the session so far:\n\n" + summary + preserved(older)}},
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
