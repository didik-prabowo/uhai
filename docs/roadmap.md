# Roadmap

What is built, what is next, and — for everything not built — the trigger that
should start it. A phase is not a schedule. It is a promise that the work
underneath it is finished enough to build on: nothing here moves forward until
what it stands on holds weight.

The rule this file keeps: **a phase ships when something real uses it**, not
when the checklist is ticked. Two phases below were declared done only after a
second implementation proved the first one had the seams in the right places.

---

## Phase 1 — A terminal that can hold a conversation *(done)*

The skeleton, and the loop that makes it an agent rather than a chat window.

- `cmd/uhai` parses flags and nothing else; `internal/orchestrator` is the one
  place that says which parts make up a running uhai.
- Two front ends behind one set of commands: a bubbletea program for a
  terminal, a prompt-per-line for a pipe.
- The agent loop — send, read tool calls, run them, send the results back —
  with history compaction when the window fills.
- Six tools: `read_file`, `write_file`, `edit_file`, `glob`, `grep`,
  `run_bash`.
- `internal/task`: a registry, two jobs at once, the rest queued; `/bg`,
  `/check`, `spawn_task` and `/stop` all reaching it through `Registry.Run`.
- One conversation per file under `~/.uhai/sessions`, named by an id that is
  the moment it started. `-sessions` lists them; `-resume <id>` takes any
  prefix that names only one, and brings back the model the conversation was
  held with rather than whatever `settings.json` says today.

**Declared done because** three different callers went through `Registry.Run`
without any of them learning about the queue, and neither kind of task needed
an `Agent` interface to exist. A function was the seam.

**Deliberately not in it:** `MockAgent`. The tests fake `provider.Provider`
instead, so the agent loop is exercised rather than replaced.

## Phase 2 — More than one provider *(done)*

- `internal/provider` as the contract: neutral content blocks, a stop reason,
  token usage, a hook for streamed text.
- Three implementations split by **wire format, not by company** —
  `openai` (Chat Completions: Groq, OpenAI, OpenRouter, Ollama),
  `anthropic` (Messages), `gemini` (generateContent).
- A shared `provider.Post` with backoff, because two copies of a retry loop is
  one too many.
- `config/models.go`: context window, max output, tool support, price per
  million tokens, matched by family prefix.

**Declared done because** the third format landed without changing a neutral
type. It cost one map — Gemini matches a tool result by *name* where everyone
else uses an id — and turned `if name == "anthropic"` into a table column.

**Still open:** the Anthropic and Gemini clients have only ever run against
fake endpoints. See Phase 4.

## Phase 3 — Permission, and documents that cannot drift *(done)*

- Three rule lists — `allow`, `ask`, `deny` — in the shape Claude Code uses.
  Deny beats ask beats allow, longest specifier wins, so file order decides
  nothing.
- A shell line is split at `&&`, `||`, `;`, `|` and judged part by part; a
  command that builds itself with `$(...)` is never allowed silently.
- The question shows what will happen: a path and a diff for an edit, the
  command itself for a shell call.
- `docs/tools.md` and `docs/permissions.md`, held to the code by
  `internal/tools/rules_test.go` — a tool without a section, or a table that
  disagrees with `NeedsConfirm`, fails the build.
- A project written for Claude Code is read as it is: `CLAUDE.md`/`AGENTS.md`
  with `@imports`, and skills under `.claude/skills`.

---

## Phase 4 — Real work, real keys *(next)*

The only phase here with no design in it. Everything above was verified
against servers we wrote ourselves.

- [ ] One turn through Anthropic and one through Gemini on a live key. A fake
      endpoint proves the shape of a request, never that the vendor agrees
      with it.
- [ ] Fix the model registry against what the APIs actually list — starting
      with groq's default `llama-3.3-70b-versatile`, which answers `404 does
      not exist` on a real key.
- [ ] Use it for a day of ordinary work and fix what that breaks, in the order
      it breaks.

**Done when** a working day goes by without dropping back to another tool.
Everything below waits for what this phase teaches: a roadmap written before
the first real use is a list of guesses.

## Phase 5 — The gaps a day of use will name

Written down now so they are recognised when they appear, not to be built in
order. Each is small; the point is that *use* picks which.

- **Editing more than one place at a time.** `edit_file` replaces one unique
  match. A refactor across six files is six confirmations.
  *Build it when a single change routinely takes more than three edits.*
- **Reading the web.** No tool fetches a URL, so a stack trace mentioning a
  library's docs ends the trail. *Build it when a session is regularly
  interrupted to paste a page in.*
- **Images.** No tool takes one, no client sends one, and the model picker no
  longer claims otherwise — it advertised "images" on models uhai had no way
  to show an image to. A screenshot of a broken layout is the case that would
  earn it. *Build it when a bug is being described in words that a picture
  would have settled.*
- **A visible plan.** Long jobs are a wall of tool calls with no shape.
  *Build it when a turn's steps stop fitting in the status row.*
- **Incremental task output.** `/tasks t1` keeps the last 8,000 characters, and
  a task that fails keeps them too rather than reporting nothing. *Build the
  incremental version when a task prints thousands of lines and the tail stops
  being enough.*

## Phase 6 — Work that outlives the process *(triggered, not scheduled)*

A conversation survives being closed; the work inside it does not. Killing
uhai kills the tasks with it — `/stop` takes the whole process group on
purpose — and nothing about a task is on disk. That is a shape, not an
oversight: a task here is a side quest that reads ten files so the main
conversation pays for one report, and a side quest whose parent is gone has
nobody to report to.

Making it durable means a different model of work, and these three items are
that model arriving one piece at a time:

- **`tasks.json`.** What ran, what it said, whether it finished. *Build it when
  a task is long enough that losing one hurts* — today the longest is `/check`,
  which is cheaper to re-run than to resume.
- **Retrying an interrupted task.** Needs the above, plus an answer to the
  question that makes it hard: a task killed halfway may have already written
  files or pushed a commit. Re-running it is not obviously safer than dropping
  it. *Build it when tasks are read-only or idempotent by construction* — which
  is what typed agents below would buy.
- **A workflow: named steps, a status per step, resume from the first
  unfinished one.** This is the item the other two are really for. It is also
  Phase 5's *visible plan* seen from the other end — one wants to show the
  shape of a turn, the other wants to survive losing it. *Build it when a
  single job routinely spans more than one sitting.*

Until then the durable thing is the conversation, and it is enough: the history
comes back, the model comes back, and re-asking is one arrow key.

## Phase 7 — Splitting the work *(triggered, not scheduled)*

- **Typed agents** (`.uhai/agents/*.md`: a name, a prompt, its own tools and
  model). Buys permission per kind — a reviewer with no `edit_file` cannot
  write, as a fact rather than an instruction — and a cheap model for cheap
  work. Costs a frontmatter parser, definition loading, tool-name validation
  and a type argument on `spawn_task`, some 250 lines.
  *Build it when two kinds of background work genuinely need different
  treatment.* Until then `/bg` is already read-only, which is the useful half.
- **A model per task.** Everything runs the session's model. *Build it when
  the cost of background work is worth splitting — the status row prices each
  turn, so the moment will be visible.*
- **Continuing a task.** Tasks are one-shot: they run, they report. *Build it
  when a task becomes a conversation of its own.*

## Phase 8 — Tools that are not ours *(not started)*

MCP would let a project hand uhai its own tools — a database, an issue
tracker — without any of them being written here. It is the one item on this
page that changes the shape of `internal/tools` rather than adding to it: a
tool list that is discovered at runtime, and a permission rule for a name
nobody wrote down.

*Build it when a project needs a tool that does not belong in this binary.*
Not before: an integration nobody has asked for is a protocol implementation
with no users.

---

## Not on this page, on purpose

Things that look like roadmap items and are not.

- **A plugin system.** Skills already answer "teach it about my project"
  without any code loading.
- **A config UI.** `settings.json` is read fresh on every check; an editor for
  a file the user already has open is machinery to undo a default.
- **Windows support.** Nothing in the code refuses it, nothing has tested it.
  *Build it when somebody runs it there and says what broke.*
