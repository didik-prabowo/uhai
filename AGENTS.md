# ouhai

A CLI coding agent. `cmd/ouhai` is the entry point and holds nothing but flag
parsing; everything else lives in `internal/`.

These are the things the code cannot tell you: what was decided, what was tried
and rejected, and what is deliberately unfinished. Everything else — structure,
naming, how a function works — read from the source, which is commented for it.

## Running and testing

    CGO_ENABLED=0 go test ./...

The `CGO_ENABLED=0` is not optional here: the sandboxed linker cannot build cgo
test binaries, and the failure it produces says nothing about your change.

`go build -o ouhai ./cmd/ouhai` after touching the UI, then restart the running
session — a session started before the build behaves like the build it was
started with. The welcome box prints the binary's build time so that mistake
takes one glance to spot.

## Two front ends, and which one runs

`app.go:Run` looks at stdin and picks:

- `tea.go` — a bubbletea program on the alternate screen, for a terminal.
- `pipe.go` — a prompt per line, answers on stdout, for a pipe. Nobody is there
  to answer a confirmation, so tools that write or run commands are refused.

`commands.go` holds what both do — the command list, the providers, the session
on disk, the background tasks — with no printing in it, so neither front end
has to know how the other says things. `text.go` measures and cuts text for
either.

There used to be a third: a renderer that painted the terminal by hand, from
before bubbletea. It was deleted once the new prompt could do everything it
did. It is not in this history — the branch that built all this was squashed
into one commit — but the tag `backup/before-squash-1146` still points at those
73 commits if a decision needs checking.

## Providers

`internal/provider` is the contract: neutral content blocks, a stop reason,
token usage, and a hook for streamed text. `agent` speaks only to it and never
imports a vendor.

Two implementations sit behind it, and the split is by *wire format*, not by
company:

- `provider/openai` — Chat Completions, which Groq, OpenAI, Gemini's compat
  endpoint, OpenRouter and Ollama all speak. Only the base URL differs.
- `provider/anthropic` — the Messages API. Blocks rather than a flattened
  string, `max_tokens` required, and a tool call whose arguments arrive as
  fragments of JSON that are only valid once the block closes.

`config.loadProvider` picks between them on the provider's name. It is one
comparison against a literal on purpose: a table of constructors is what the
third wire format should buy, and there is no third yet.

What the second implementation taught, and what a third will have to settle:

- `provider.Request` has no `MaxTokens`. Anthropic needs one, so the caller
  passes it in the client's options, out of the registry below, and the neutral
  request stays as it was.
- `Request.Stream` carries text only. Tool arguments stream too, and are
  buffered rather than reported — fine while nothing shows them being typed.

## Permission

The code is split the way the concerns are: `internal/tools` has `tool.go` (the
list the model is given, and dispatch), `files.go`, `search.go`, `shell.go`, and
`permission.go` — five lines saying which tools escape this process. In
`internal/cli`, `permission.go` answers "may this run, and how is it asked",
both halves in one place, because an answer is worth nothing if what it
approves cannot be read.

`docs/tools.md` is the page for users, and `internal/tools/rules_test.go` holds
it to the code: a tool added without a section, or a permission table that
disagrees with `NeedsConfirm`, fails the build. Change the behaviour and the
page in the same commit, because the tests will make you anyway.

Tools that write files or run commands ask first (`tools.NeedsConfirm`), and
the question shows what will happen: a path and a diff for an edit, the command
itself for a shell call. A blob of JSON can only be trusted; a diff can be
judged.

`permissions` in the settings is three lists of rules — `allow`, `ask`, `deny`
— in the shape Claude Code uses, `Bash(git push:*)` and `Read(*.env)`. Deny
beats ask beats allow, and the longest specifier wins, so ordering in the file
decides nothing. A tool denied outright is never offered to the model —
`agent.AllowTool` filters the list before it is sent — and refused if called
from memory anyway. The agent takes that as a function rather than reading
settings itself, which is what keeps `internal/agent` free of
`internal/config`.

A shell line is split at `&&`, `||`, `;`, `|` and newlines and judged part by
part, because a prefix rule otherwise allows everything after the first
command; and a command that builds itself with `$(...)` is never allowed
silently, since the rules can only judge what they can read.

Two ways to stop being asked, and they are deliberately different:

- `permissions` in the project's `.ouhai/settings.json`, read fresh each time,
  so editing it takes effect at once.
- `a` at the prompt allows that tool for the rest of the session only. It is
  in memory, so nothing a session waves through outlives it.

The question is asked from the agent's goroutine while the model works, which
is why it outranks the spinner in the status row and why the session's list
carries its own lock.

## The model registry

`config/models.go` answers four questions about a model, and each is answered
because something asks it: how much history fits before `agent` compacts, how
long an answer may be (the Messages API refuses to guess), whether tools may be
sent at all, and what the turn costs — shown in the model picker and in the
status row while the model works.

Prices are list prices per million tokens, and only for models sold by the
vendor that made them. The same open model costs different money at Groq, at
OpenRouter, or on a machine under the desk, so those carry no price and are
simply not priced: no figure beats a confident wrong one.

It is matched by prefix against the last segment of the name, so a family is
one entry — `claude-`, not one line per release — and
`openrouter/meta-llama/llama-3.3-70b-instruct` finds `llama-3.3` all the same.
Anything unlisted falls back to figures small enough to be safe anywhere: being
wrong low costs an early compaction, being wrong high costs the turn.

The window follows the model, not the session, so `/model` and `/connect` move
it (`useProvider` in tea.go). Add a family when a model behaves oddly, not
because the table looks short.

## Tasks

`internal/task` is the bookkeeping — id, status, elapsed, report — and the
queue. Two tasks run at once (`MaxRunning`); the rest wait, showing as `queued`
in `/tasks`. Work somebody is waiting on skips the queue entirely
(`RunNow`, used by the model's `spawn_task`): queueing it behind two long
background jobs stalls the conversation, and looks exactly like a model
thinking very hard. The limit is not about the machine: ten background prompts on one
key are ten simultaneous requests, and the provider answers 429 to all of them.
Work is not faster for being started at the same time.

Elapsed is time spent working, not time spent waiting, so a task that sat in
the queue does not report the wait as its own.

Three doors go through `Registry.Run`, which is why none of them had to learn
about the queue: `/bg` (a prompt run by a nested agent), the model's own
`spawn_task`, and `/check`.

`agent.Asker` names the interface an agent would have — `Ask` and nothing else
— with a compile-time assertion that the agent satisfies it. Nothing consumes
it, which the comment says plainly. It is a marker for a seam that already
exists as a function, not a second one.

`/check` is the other kind of work entirely — a command, no model anywhere in
it, which is why it runs with no provider connected. It is also the answer to
"should there be an Agent interface": two kinds of task now exist and neither
needed one, because `task.Runner` is already the seam. A function was enough.

When a task ends, its result reaches both the user and the model: the line in
the chat, and the report folded into the next prompt, so a failing check is
something the model can answer for rather than something you retype. It travels
with the prompt rather than as a message of its own — the Messages API wants
the roles to alternate, and two user messages in a row would be refused — and
the chat says when it is being sent.

A task can be watched while it works — `/tasks t1` shows what it has printed so
far, line by line for a command and as the answer is written for a prompt — and
stopped with `/stop t1`, queued or running. Stopping kills the command's whole
process group: killing the shell alone leaves `go test` compiling in the
background, which is not what stopping means.

What `/check` runs is the project's own business: `check` in its
`.ouhai/settings.json`, or a guess from the files present (go.mod, package.json,
Makefile) when it says nothing.

## Drawing

Every colour comes from `theme.go` — slate for structure, one violet accent,
softened red and green for a diff — and a test fails the build if a view writes
one of its own. That test exists because the palette had already drifted across
two files once.

Four rules the layout keeps, each of which took a bug to learn:

- **Nothing fills the last column.** A line as wide as the terminal makes it
  wrap on its own, which scrolls the screen under the renderer. Everything is
  built to `m.cols()`, one short.
- **Nothing is drawn on the last row.** Once the chat is long enough to scroll,
  that row is the one the renderer loses track of; it is left blank on purpose.
- **A line that already fits is never re-wrapped.** Narrowing it to make room
  for a hanging indent is what turned the welcome box into rubble.
- **Anything the chat draws to a fixed width is cut by columns, not by
  characters.** A coloured line is mostly escape codes.

The rhythm is a blank line before each question, one after it, and one before
the line that closes the turn. A tool call is one row, cut to the width: it is
a note that something happened, not the thing itself.

## The terminal bargain

Every layout question here resolves to one chain, and it was walked in both
directions before settling:

Pinning the prompt to the bottom needs the alternate screen. The alternate
screen has no scrollback, so the app must scroll the chat itself. For that the
wheel has to reach the app, which means asking the terminal for mouse reports.
And a terminal reporting the mouse no longer selects text on a plain drag.

What was chosen: pin the prompt, take the mouse, and lean on the modifier the
terminals already have — shift with a drag selects. `/mouse` hands the mouse
back whole for terminals where that is not enough.

What was tried and rejected:

- **Inline output, the way Claude Code works.** Scrolling and selecting are the
  terminal's and nothing needs configuring, but the prompt is content too and
  moves when the chat is scrolled. Rejected: the prompt staying put was worth
  more.
- **Alternate scroll (`DECSET 1007`).** The terminal turns the wheel into arrow
  keys without reporting the mouse, which would have bought both. tmux 3.6
  removed the option, so inside tmux it does nothing — and the arrows are the
  prompt's history, so it would have had the wheel typing into the box.
- **A saved mouse mode in settings.json.** Machinery to undo a default; the
  default was fixed instead.

Consequences worth knowing: inside tmux a shift+drag is the terminal's own
selection and spans the panes, since the terminal knows nothing about splits.
Pane-aware selection is tmux's — `prefix + [`, then drag. `/help` says so.

## Keys

Escape-sequence keys travel through the terminal, the multiplexer and terminfo,
and any of them may swallow one; plain control characters do not. That is why
scrolling is on `^y`/`^e` and not only on shift with the arrows.

The arrows recall past prompts, which is what a shell does with them, and the
page keys do the same — on a Mac they are `fn` with the arrows, and the hand
reads them as the same key.

## Looking things up

`gh` is installed and logged in, so anything on GitHub is a shell command away:
`gh pr list`, `gh pr diff`, `gh issue view`, `gh run list`, `gh repo view`.
Dependencies are the same — `go list -m -u all` for what could be updated,
`go mod why` for what pulls something in.

The read-only ones are in the project's `allow` list, so they run without
asking. Anything that changes a repository — opening a pull request, merging,
`gh api -X POST` — is not, and asks. That line is deliberate: reading the world
is free, changing it needs a human.

## Project notes and skills

`OUHAI.md`, `AGENTS.md` or `CLAUDE.md` — first one found, each checked in a
`.local` variant first — is read into the system prompt every session, with
`@path` lines replaced by the file they name, three deep and cycle-guarded. The
`.local` files and the `@` imports are conventions rather than anything the
AGENTS.md standard defines; they are supported because projects use them. Skills are the other half: a folder
per skill with a `SKILL.md` under `.ouhai/skills/` or `.claude/skills/`, of
which only the name and description reach the prompt. The body is a path the
model reads when the work calls for it, which is what keeps a project's fifty
pages of procedure from costing anything on a turn that does not need them.

Both layouts are Claude Code's, deliberately: a repository already written for
one agent should need nothing added for this one.

## Not built, on purpose

Each of these was considered, argued for, and left out. The trigger matters
more than the verdict: build it when the trigger fires, not because the list
looks short.

- **Typed agents** (`.ouhai/agents/*.md`: a name, a prompt, its own tools and
  model). Buys permission per kind — a reviewer with no `edit_file` cannot
  write, as a fact rather than an instruction — and a cheap model for cheap
  work. Costs a frontmatter parser, definition loading, tool-name validation
  and a type argument on `spawn_task`, some 250 lines. *Build it when two kinds
  of background work genuinely need different treatment.* Until then `/bg` is
  already read-only, which is the useful half.
- **MockAgent.** The tests fake `provider.Provider` instead, so the agent loop
  itself is exercised rather than replaced. A mock at the agent level would
  delete the part most worth testing. *Build it when something needs an agent
  that is not an agent — a remote one, or one that is not a model.*
- **Incremental task output** (only what is new since last read, filtered).
  Ours keeps the tail, 8,000 characters of it. *Build it when a task prints
  thousands of lines and the tail stops being enough.*
- **Continuing a task** (sending it another message, keeping its context).
  Tasks are one-shot: they run, they report. *Build it when a task becomes a
  conversation of its own.*
- **A model per task.** Everything runs the session's model. *Build it when the
  cost of background work is worth splitting — the status row now prices each
  turn, so the moment will be visible.*

## Conventions

- Comments say why, not what. Match the density already there.
- A test that waits for a goroutine polls at 5ms for five seconds
  (`waitTries`, `waitFor`). The answer arrives in milliseconds; the budget is
  only there for a machine busy with something else, where a tight one fails
  and teaches nobody anything.
- A deliberate shortcut with a known ceiling is marked `ponytail:` with the
  ceiling and the upgrade path, so it can be found later.
- Commit messages carry the reasoning, including what was rejected. This file
  is the summary; `git log` is the record.
