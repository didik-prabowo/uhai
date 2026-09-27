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
- Two implementations split by **wire format, not by company** —
  `openai` (Chat Completions: OpenAI, Gemini and any gateway) and `anthropic`
  (Messages). There was a third for Gemini's
  generateContent; it went once the compat endpoint proved it could carry a
  thought signature. Two providers ship with an endpoint; anything else is a
  base URL and a key, which is what `/connect` → `+ custom endpoint` writes.
- A shared `provider.Post` with backoff, because two copies of a retry loop is
  one too many.
- `config/models.go`: context window, max output, tool support, price per
  million tokens, matched by family prefix — and, since a gateway answered as
  `openrouter/dpe-glm-5.2`, by a family found inside the name when the prefix
  misses. That second pass takes the figures and never the identity, so a
  gateway cannot become `Known` and cannot be quoted a vendor's price.

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
- `docs/guide/tools.md` and `docs/guide/permissions.md`, held to the code by
  `internal/tools/rules_test.go` — a tool without a section, a table that
  disagrees with `NeedsConfirm`, or a tool count written out in prose and left
  behind, fails the build.
- A project written for Claude Code is read as it is: `CLAUDE.md`/`AGENTS.md`
  with `@imports`, and skills under `.claude/skills`.

---

## Phase 4 — Real work, real keys *(next)*

The only phase here with no design in it. Everything above was verified
against servers we wrote ourselves.

- [ ] One turn through Anthropic and one through Gemini on a live key. A fake
      endpoint proves the shape of a request, never that the vendor agrees
      with it.

      **Half of it is now done, and the half that is left is money.** On
      2026-09-26 the Anthropic client reached the real API for the first
      time: `/v1/models` answered with twelve model ids, and a turn came back
      `HTTP 400 — Your credit balance is too low`. So the credentials, the
      identity-linked workspace header and the request shape are all right,
      and the vendor's own sentence reached the user in one line after one
      second, with no retry — which is the behaviour a wallet error is
      supposed to get, observed rather than assumed.

      What is still unproven needs credit, not code.

      And what is left is the bigger half. The Anthropic client sends
      adaptive thinking — `claude-sonnet` carries `Thinking: true` in the
      table — and replays the signed thinking blocks that come back, so the
      turn has to make **two** provider calls: a tool call, then the answer.
      One call tests nothing new. A replay that is wrong in the order,
      the signature or a dropped empty block is a 400 on the second call, and
      it is structurally invisible from here: the fake endpoint is the side
      that would have refused.

      **A gateway was tried for this and cannot stand in, which is worth
      writing down because it looks like it can.** 9router answers
      `POST /v1/messages` with the real thing — an SSE stream of
      `message_start` and `content_block_delta`, naming the model that replied
      — and accepts `x-api-key`, so uhai's own client drives it unchanged. A
      twelve-call turn ran through it: tool calls, tool results, thinking
      blocks arriving signed and going back on every subsequent call, no error.
      It proves the client works against a real Messages API endpoint, which
      had never been observed. It proves nothing about the replay.

      The probe that settles it is a *corrupted* signature. The real API
      refuses one; through the `cc/*` route it returns 200, so the blocks are
      not reaching Anthropic and nothing is checking them. The same route
      caches the history whether or not uhai marks a breakpoint, and reports a
      usage object of its own — three observations with one explanation: it is
      an agent-harness proxy that rebuilds the conversation, not a passthrough.
      Its sibling `midas-telkom-stg/*` drops `cache_control` outright, by the
      same invalid-value probe. The one route that *is* a passthrough,
      `anthropic/*`, returns Anthropic's own sentence verbatim: *"Your credit
      balance is too low"*.

      So the wall has been reached from two directions now, and it is the same
      wall. **Verification needs credit on a direct key, and a proxy in front
      of the API is the side that would have refused.**
- [x] Fix the model registry against what the APIs actually list. Anthropic
      and Gemini report their own limits, and the table now carries theirs:
      Haiku answers 64k rather than the 8k it was capped at, and Opus 4.5 and
      Sonnet 4.5 have their own entries because their families would have
      asked them for a 128k answer the API refuses.
- [ ] Size `glm-5`, `glm-5.1`, `glm-5.2` and `gpt-5`, which Z.ai and OpenAI
      list but do not measure. Needs a key with credit on it: an account that
      cannot spend cannot be asked where its ceilings are.
- [x] A way to search the web. `search_web` asks Brave: one GET, one header,
      no SDK and no provider interface behind it. The keyless sources stay
      unavailable — DuckDuckGo does not answer this machine and Mojeek
      returns a captcha — so a key is the price, and a machine without one
      gets a note rather than an error. It asks permission one step earlier
      than `fetch_url` does, because the query leaves the machine before any
      page does. `UHAI_API_KEY` deliberately does not reach it.
- [x] Let an attached terminal change the model. The model is the daemon's to
      report and the daemon's to change: /v1/health carries what its
      conversation answers with, POST /v1/model is the switch, and it is taken
      under the turn lock. /connect is still refused — it writes a credential
      into the settings the daemon reads.
- [x] A tool that lists a directory. `list_directory`, one level, no
      permission. The gap was wider than the item said: `walk` hands its
      visitor files only, so `glob` cannot name a directory under any
      pattern — the shape of a tree was reachable only through `run_bash ls`.
      A test holds that premise, and fails if glob ever learns to.
- [x] A plan or todo tool. `set_plan` takes the whole plan each call and
      returns it drawn. It stores nothing: the tool result is in the history
      the next request carries, so re-stating the plan is what keeps it
      alive, and there is no field on Agent, no lock and nothing crossing the
      daemon's socket. Showing it on screen is the version to build when
      something other than the model needs to read it.
- [x] Worker processes for the daemon. A background task is a process now, not
      a goroutine with a `recover` around it that could not have caught the
      failures worth surviving anyway — a concurrent map write is fatal, not a
      panic, and it would have taken every project's conversation with it. No
      supervisor was needed: a task is one-shot, so stdout is the report and
      the exit code is the status.
- [x] Windows — *it compiles, and nothing here has run it*. The unix-only
      pieces are behind build tags: the socket, `Setpgid`/`Setsid`/`SIGKILL`
      in bash.go, the launcher's single-flight. Ticked because the build is
      the part that can be checked from here; the `ponytail:` notes in
      `proc_windows.go` and `spawn_windows.go` name what is knowingly weaker.
      It stays untested until somebody runs it there and says what broke.
- [x] Reload the conversation when a daemon restarts. The daemon picks up the
      project's newest session when it builds a workspace, and now says so:
      health carries what it read back, and the attached terminal opens with
      a line naming it instead of a blank screen over a model that remembers
      the morning. `/clear` is refused attached rather than clearing a local
      agent that answers nothing.

      **Still open, and small:** the messages themselves are not drawn. The
      count is what makes the context visible; replaying it needs a route, a
      version, and a translation from `provider.Message` back to chat entries.
      *Build it when the count stops being enough to remember what was said.*
- [x] Ask the language server. `find_symbol` answers where a symbol is defined,
      who uses it and what implements it — by meaning, where grep answers by
      text: `grep -rnw Run` over `internal/` returns 51 lines, and the question
      "which Run" has two answers, which the tool says rather than guesses.
      Read from crush and zero, and smaller than both because it leaves out the
      half they need and uhai does not — see below.
- [ ] Use it for a day of ordinary work and fix what that breaks, in the order
      it breaks.

crush, zot and zero were read through over 2026-09-06/07, and several
decisions here changed because of them — the model registry's figures, the
cached-token accounting, the shape of `find_symbol`. Each of those says so in
its own commit; the notes the comparison was written from are not in this
repository.

**Done when** a working day goes by without dropping back to another tool.
Everything below waits for what this phase teaches: a roadmap written before
the first real use is a list of guesses.

### What the language server is not asked for

Verified against gopls rather than assumed: `initialize`, then a query with no
`didOpen` at all, and it answers from disk. uhai's edits land on disk before
the model can ask anything, so the document sync an editor needs — zero's is
305 lines of versions, mutexes and `publishDiagnostics` — buys nothing here.

That sync is the price of **diagnostics**, and diagnostics are what
`go build ./...` already answers. *Build them when a project uhai is used in
has no fast build to ask* — a large TypeScript tree is the case that would
earn it.

**Rename** is crush's other half: it has `lsp_rename` and `lsp_replace_symbol`,
which make the server an editing mechanism rather than a way of looking. Left
out because `edit_file` already edits and the model can see what it is
changing. *Build it when a rename across twenty files is being done by hand
often enough to be worth not seeing.*

**Call hierarchy** is the one gap here that neither `grep` nor `find_symbol`
stands in for, and it was found by reading crush rather than by wanting it.
crush exposes six LSP tools where this exposes one; five of those five extra are
answered another way — `lsp_definition` and `lsp_symbols` are `find_symbol`,
`lsp_restart` is a problem that only exists once a server holds document state,
and rename is the entry above. `lsp_call_hierarchy` is not answered by anything:
*"what breaks if I change this function"* is a tree, and `find_symbol
references` gives one layer of it. Reading the second layer means asking again
per caller, which is a turn each and the model has to decide when to stop.

*Build it when a change is being sized by reading references one at a time.*
The cost is small and known — `callHierarchy/incomingCalls` after
`textDocument/prepareCallHierarchy`, no document sync, so the same shape
`find_symbol` already has — which is what makes it the next thing here rather
than diagnostics.

**And the comparison the other way, since a table of missing features only runs
one direction.** Six tools is six names a model has to keep straight, and 26
server entries is 25 claims that a command spelled that way answers the
protocol — where a wrong entry fails exactly like a server nobody installed.
The list here is four because four can be checked by hand, and only one of them
has been. That is a smaller promise, kept.

## Phase 5 — The gaps a day of use will name

Written down now so they are recognised when they appear, not to be built in
order. Each is small; the point is that *use* picks which.

- **Editing more than one place at a time.** `edit_file` replaces one unique
  match. A refactor across six files is six confirmations.
  *Build it when a single change routinely takes more than three edits.*
- **A secret with no shape, in a file with an ordinary name** *(the half of
  credential handling deliberately left)*. A tool result goes four places — the
  model, the history, the next request to a provider, and the session file in
  plaintext — and two of the three ways a credential gets there are closed. A
  file that is a credential *by convention* is refused by name: `.env`, `*.pem`,
  `id_rsa*`, anything under `.ssh/`. A credential with a *recognisable shape* is
  taken out of every tool result, because `ghp_`, `AKIA` and `sk-ant-` are
  prefixes their vendors reserve, so matching them is recognition and not
  inference.

  What is left is `password: hunter2` in `config/production.yaml`, and it leaks
  through `read_file`, `run_bash` and `grep` — measured, not assumed.

  It is not built because the obvious rule is worse than the gap.
  `(?i)(password|token|secret)\s*[:=]\s*\S+` also matches
  `password := getPassword()`, and those false positives land on source code,
  which is what this program reads all day. A wrong redaction is not a harmless
  over-reaction: the model reads a marker where a value was and sets about
  repairing a file that is fine. That was observed while the shape-matching was
  being built, before word boundaries went on the patterns — a CI URL ending
  `/task-a1b2c3…` came back redacted as an OpenAI key.

  *Build it when a secret has actually reached a provider through content with
  no recognisable shape* — a real case, not a constructed one, since the
  construction is easy and proves nothing about whether it happens. And when it
  does, the first thing to reach for is **not** content inference: it is another
  name on the refused-by-convention list, because a real case names a real file
  and a name cannot be wrong about what it matches.
- **Running a batch of tools at once** *(measured, not built)*. `runTools`
  walks one response's tool calls in a single loop, while the system prompt asks
  the model to batch the independent ones — so it does, and they run in series.
  Measured on this repository: four greps take 13ms one after another and 5ms
  together. The ratio flatters it; the saving is eight milliseconds against a
  turn that waits seconds for a model. The cost scales with the tree, since a
  grep reads every file in it, so at a hundred times this size each one
  approaches its own fifteen-second ceiling and four of them is a minute.
  *Build it when a batch of reads is visibly slower than one of them.*

  The shape, written down because it is not the obvious one: **effects serial,
  looking parallel**. `NeedsConfirm() == false` is exactly the set of tools whose
  effects do not escape the process, so those are safe together by construction,
  and a confirmation is serial anyway because a person answers it — which is why
  a batch containing `run_bash` was never the case to build for. Parallelise
  *consecutive* runs of eligible calls rather than all of them, or a batch of
  `read_file` then `edit_file` on one path changes meaning; and leave
  `OnToolCall` firing in block order, since it draws to the screen.
- **Reading the web** *(built)*. `fetch_url` opens an address and `search_web`
  finds one, so a stack trace mentioning a library's docs no longer ends the
  trail at either end. Searching needs a key; without one the tool says where
  to get it rather than failing, and `fetch_url` still works on an address you
  already have.
- **Images.** No tool takes one, no client sends one, and the model picker no
  longer claims otherwise — it advertised "images" on models uhai had no way
  to show an image to. A screenshot of a broken layout is the case that would
  earn it. *Build it when a bug is being described in words that a picture
  would have settled.*
- **A visible plan.** Half of this arrived as `set_plan`: the model has
  somewhere to keep a plan, and re-stating it each call is what keeps it
  alive. The half still missing is the screen — a long job is still a wall of
  tool calls to the person watching it. Drawing it costs what `set_plan`
  deliberately refused: state on the Agent, a lock around it, and a route
  across the daemon's socket. *Build it when the person watching needs to know
  where a turn has got to, rather than the model needing to remember.*
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
