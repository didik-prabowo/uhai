# uhai

A CLI coding agent. `cmd/uhai` is the entry point and holds nothing but flag
parsing; everything else lives in `internal/`.

These are the things the code cannot tell you: what was decided, what was tried
and rejected, and what is deliberately unfinished. Everything else — structure,
naming, how a function works — read from the source, which is commented for it.

## Running and testing

    CGO_ENABLED=0 go test ./...

The `CGO_ENABLED=0` is not optional here: the sandboxed linker cannot build cgo
test binaries, and the failure it produces says nothing about your change.

`go build -o uhai ./cmd/uhai` after touching the UI, then restart the running
session — a session started before the build behaves like the build it was
started with. The welcome box prints the binary's build time so that mistake
takes one glance to spot.

## Two front ends, and which one runs

`app.go:Run` looks at stdin and picks:

- `tea.go` — a bubbletea program on the alternate screen, for a terminal. It
  was one 1,455-line file, a fifth of the whole project, and is now four:
  `tea.go` is the model and the loop, `tea_view.go` what the screen looks like
  and the arithmetic that fits it to the window, `tea_pickers.go` the lists and
  forms that open over the chat, `tea_prompt.go` what happens when a line is
  submitted.
- `pipe.go` — a prompt per line, answers on stdout, for a pipe. Nobody is there
  to answer a confirmation, so tools that write or run commands are refused,
  and a slash command is answered here rather than sent to the model: `/help`
  works, the rest are refused by name. Sending one as a question spent a turn
  to be told it was not a question.

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

Three implementations sit behind it, and the split is by *wire format*, not by
company:

- `provider/openai` — Chat Completions, which Groq, OpenAI, OpenRouter, Z.ai
  and Ollama all speak. Only the base URL differs, which is why five vendors
  share one folder: a `provider/zai` would have been the same file twice.
- `provider/anthropic` — the Messages API. Blocks rather than a flattened
  string, `max_tokens` required, and a tool call whose arguments arrive as
  fragments of JSON that are only valid once the block closes.
- `provider/gemini` — generateContent. The assistant is called "model", a call
  and its result are parts of a message, and a result is matched to its call by
  the tool's *name* — so the id every other API hands out has to be looked back
  up while translating, which is the one place the neutral types cost something
  to carry.

`config.loadProvider` picks by `API` in the provider table, which the third
format bought: a name compared against a literal was fine for two.

What the second and third implementations taught:

- `provider.Request` has no `MaxTokens`. Anthropic needs one, so the caller
  passes it in the client's options, out of the registry below, and the neutral
  request stays as it was.
- `Request.Stream` carries text only. Tool arguments stream too, and are
  buffered rather than reported — fine while nothing shows them being typed.
- `Request.Reasoning` carries a thinking model's working out, which the
  vendors send apart from the answer under two different names
  (`reasoning_content`, `reasoning`) and which is emphatically not the answer:
  it is shown dimmed, never kept, never sent back. Mixed into `Stream` it
  would be spoken as the reply and stored in the history as one. GLM made this
  visible — twenty seconds of silence that looked exactly like a hang.
- Tool calls are identified by an id the neutral types require and Gemini does
  not have. That client invents one, which works because the id only has to be
  unique within the conversation it is used in.

The Anthropic client marks the end of the system prompt as a cache
breakpoint. The API renders tools, then system, then messages, so one mark
covers every byte of a request that does not change within a turn — for this
project about nine thousand tokens, resent on each iteration of the tool loop,
so ten tool calls used to mean paying ten times for identical text. That is why
`System` goes as a block rather than a string: a string cannot carry the mark.

One breakpoint, not the four the API allows. The next one worth having is on
the conversation so far, and it has to move every turn; this one never moves,
which is what makes it free to keep correct.

`/cost` is the other half of that. The status row prices a *turn*, which is the
wrong number for "what have I spent": a turn with ten tool calls is charged for
its input ten times and only the last one is ever on screen. `spent` adds every
call up, `/cost` reads it, and it is printed on the way out beside the resume
id — the moment the question actually gets asked. Tokens served from cache get
their own figure there, since that is the difference between this session and
the same session without a breakpoint.

It is in memory and starts again with the process. Persisting it would mean
storing tokens and pricing them later at whatever model happens to be loaded
then, which is a confident wrong figure the first time `/model` is used.

`Usage` counts cached input apart from fresh input, because it is not billed
the same — a read is a tenth, a write a quarter extra, and `config.CostUSD`
prices all three. Folding them together would have made the status row quote a
figure wrong by roughly the whole system prompt, in whichever direction caching
happened to work.

Compaction fires at 85% of the window, not at all of it. Input and output
share the window, so a history that exactly fills it leaves nowhere for the
answer to go and the provider refuses the request rather than trimming it — the
old rule summarised only once the history had already passed the whole window.
The fifteen per cent held back is larger than the longest answer any model here
may write (150k against a 128k ceiling on a million-token window; 4.8k against
4,096 on the 32k fallback), so the headroom is arithmetic rather than a guess.
It also absorbs `Tokens` being loose: characters over four is close for prose
and undercounts code and JSON.

## When a turn does not finish

A turn fails in the middle more often than anything else here: a rate limit, a
header timeout, esc. Whatever the cause, `Ask` leaves the history ending on a
**user** message — the prompt nothing answered, or the tool results nothing
read — and the Messages API refuses two user messages in a row. So the failure
was silent and the *next* prompt was refused, which is the worst kind of error
to debug: the message names a rule, not the turn that broke it.

`closeTurn` ends the turn with an assistant message carrying whatever was
streamed plus one line saying why it stopped. Three things fall out of it: the
history alternates again, the history agrees with what was on the screen, and a
model asked to carry on can see how far it got. The tokens were paid for
either way.

Retrying is the other half, and it is deliberately uneven: a 429 waits five
seconds and then ten, a 5xx one and two, and a transport error — a timeout, a
reset — is not retried at all, since the three minutes it already waited are
the evidence that waiting is not the answer.

## Permission

`tools.Tool` is an interface — `Name`, `Description`, `Schema`, `NeedsConfirm`,
`Run` — so a tool need not live in `internal/tools`. Every tool is a type implementing it, one per file: `readTool` in `read.go`,
`bashTool` in `bash.go`. There is no privileged way to write a built-in — the
adapter struct that used to let them be one-line literals is gone, and a tool
in this repository is written exactly the way a tool from an MCP server would
be. Four of the five methods return a constant, which looks like ceremony until
you notice it is the same ceremony an outside implementer pays; a shortcut only
the built-ins could take is a second design nobody else can follow.

Sub-packages — `tools/files/read.go` and so on — were considered and are the
shape to reach for later, not now. `tools/files` would need `tools.Tool` and
`tools` would need `tools/files` to fill `all`, which is an import cycle; Go's
structural typing dodges it, but only by putting the `tool` struct out of
reach, so every tool would write the five methods itself and the ceremony this
design avoids comes back. `walk` and `Shell` are shared across what would be
two of those packages as well. The trigger is a group with its own
dependencies or its own *source* — `internal/tools/mcp` will be a real
sub-package, because a tool from a server is genuinely not one of these.

`all` still names them in order, rather than seven `init()` calls registering
themselves. Self-registration would leave the order to whatever order the files
happen to initialise in, and that order is the order the tool schemas reach the
model — which is inside the cached prefix. It would also turn a duplicate name
from a returned error into a panic at startup. `Register` is how a tool from elsewhere joins, and it
refuses a name already taken: two tools answering to one name is a bug the
model experiences as the wrong thing happening, with nothing to read. The list
is behind an `RWMutex` — an MCP server reconnecting mid-session would register
from its own goroutine while the agent reads the list from its, and a registry
that is only safe when used the way its author imagined is not safe.
`database/sql.Register` locks for the same reason.

`Run` returns `(result string, isError bool)` and not `(string, error)` on
purpose: the bool is the `is_error` flag on the wire, not a failed function.
A tool that cannot do its job has still done its job by saying why — "could
not read file: no such file" is something the model can act on, where a
dropped result leaves it believing the file was empty.

The interface cost 22 lines over the plain table it replaced, and giving every
tool its own type cost four more — cheap, because one file per tool had already
removed the duplication that made the same change expensive when they shared
one. It buys one
thing the table could not do at all — a tool whose name is not known until the
program runs, which is what an MCP server or a plugin would be. `external_test`
is the proof, and the only test that could be: it defines a tool outside the
package, registers it, and reaches it through `Definitions`, `NeedsConfirm` and
`Execute`. A built-in would pass that test even if `Tool` were a struct.

The code is split the way the concerns are: `internal/tools` has `tool.go` (the
contract, the registry and dispatch), one file per tool — `read.go`,
`write.go`, `edit.go`, `glob.go`, `grep.go`, `bash.go`, `fetch.go`, each
holding a tool's schema, description, permission and implementation together —
`walk.go` for what glob and grep share, and `permission.go` — five lines saying which tools escape this process. In
`internal/cli`, `permission.go` answers "may this run, and how is it asked",
both halves in one place, because an answer is worth nothing if what it
approves cannot be read.

`docs/tools.md` is the page for users, and `internal/tools/rules_test.go` holds
it to the code: a tool added without a section, or a permission table that
disagrees with `NeedsConfirm`, fails the build. Change the behaviour and the
page in the same commit, because the tests will make you anyway.

`fetch_url` is the fourth tool that asks, and the reason is a direction rather
than an effect. It reads, which sounds like the free half of "reading the world
is free, changing it needs a human" — but it reads by *sending*: the URL goes to
somebody else's server, and the URL is the model's to compose. Unasked it would
have been the way around `run_bash`'s confirmation, the same egress through the
door that does not stop. `Fetch` in the `allow` list turns the asking off for
anyone who would rather have it off.

Most of `web.go` is about where it refuses to go. Loopback, the private ranges
and link-local are checked before the request *and* on every redirect, because
checking only the first address and then following wherever it leads is not a
check — an open redirect on a public host reaches `169.254.169.254` in one hop,
and that address hands out a cloud machine's credentials. `file://` is refused
because it would be a second `read_file` with none of its rules.

Tools that write files or run commands ask first (`tools.NeedsConfirm`), and
the question shows what will happen: a path and a diff for an edit, the command
itself for a shell call. A blob of JSON can only be trusted; a diff can be
judged.

The diff matches lines properly — a common head and tail, then a longest common
subsequence over what is left, with a budget past which a rewrite is simply
shown as one. It began as head-and-tail matching alone, which made one changed
line in the middle of a function read as the whole function being replaced. That
is not a cosmetic difference: a diff that cries wolf is answered with `y`
without being read, and the diff is the only reason the question beats a blob of
JSON.

`permissions` in the settings is three lists of rules — `allow`, `ask`, `deny`
— in the shape Claude Code uses, `Bash(git push:*)` and `Read(*.env)`. Deny
beats ask beats allow, and the longest specifier wins, so ordering in the file
decides nothing. A tool denied outright is never offered to the model —
`agent.AllowTool` filters the list before it is sent — and refused if called
from memory anyway. A spawned agent inherits that function along with
`Confirm`: it used to inherit only the asking, which meant a background task
was handed tools the settings had refused. A denial is not a question asked
again in a window nobody is watching.

Two tests hold the wiring together, both added after a question exposed how
little was holding it: every tool in `Definitions` must actually reach a case
in `Execute` — the name in the schema and the name in the switch are two
strings that only happen to match — and every tool must have an entry in
`toolNames`, or a rule naming it parses as nothing and denies nothing. The
second one found `spawn_task`, which could not be named in a rule at all. The agent takes that as a function rather than reading
settings itself, which is what keeps `internal/agent` free of
`internal/config`.

A shell line is split at `&&`, `||`, `;`, `|` and newlines and judged part by
part, because a prefix rule otherwise allows everything after the first
command; and a command that builds itself with `$(...)` is never allowed
silently, since the rules can only judge what they can read.

Two ways to stop being asked, and they are deliberately different:

- `permissions` in the project's `.uhai/settings.json`, read fresh each time,
  so editing it takes effect at once.
- `a` at the prompt allows that tool for the rest of the session only. It is
  in memory, so nothing a session waves through outlives it.

The question is asked from the agent's goroutine while the model works, which
is why it outranks the spinner in the status row and why the session's list
carries its own lock.

`config/dir.go` is the only file that names `.uhai`. It was four copies across
two packages, and the rename from ouhai survived on a global search rather than
on design — a folder name is a string, not something the compiler checks, so
one that got missed would have hidden a conversation rather than failed a
build. `Dir`, `InDir` and `ProjectDir` are what everything else asks, and a
test walks `internal/` failing any file that spells it out again. `~/.claude`
goes through `homeJoin` instead, because that folder is Claude Code's and not
ours to name.

## Sessions

One conversation, one file under `~/.uhai/sessions`, rewritten after every
turn. The name is the id, and the id says nothing: eight random bytes in
base32, thirteen characters, `gijvqxlaulhjq`. `-resume` takes any prefix that
names only one, so four characters is normally enough to type.

It used to be the start time, which read well and sorted for free — and was
wrong twice. It was only good to the second, so two conversations begun in the
same second were one file and the second to save replaced the first, whole and
without a word; a pid was pinned on the end to fix that. And a name that
encodes when a thing was made states a fact the file already carries, which
makes the copy in the name the one that can go stale — a resumed conversation
kept the date it was first opened.

So when, and who holds it, are fields — `started`, `updated`, `pid` — and
`-sessions` prints them. `pid` is rewritten on every save, because a resumed
conversation is held by the process resuming it, not by the one that opened it
in August. Sessions written before it existed show a blank rather than `pid 0`.

Nothing sorts on the file name any more; `All` sorts on `updated`. That cost
nothing: it already read every file whole to build the listing, which is the
`ponytail:` note in the same function.

The id is minted by `session.New` at startup rather than at save time, because
the prompt prints it on the way out and it has to be the string that ends up on
disk. Old date-named files still list and resume: an id missing from the JSON
falls back to the file name.

It lives in `internal/session`, not in `config`, because the two are opposites:
settings, keys and permissions are input a person writes to change what uhai
does, and a session is output uhai produces by running. It sat in `config` for
a while on the strength of sharing a directory — and nothing in `config` ever
referenced a `Session`, which is what gave the mistake away.

`Session` is the data; `Store` is the contract for keeping it — `Save`, `All`,
`Load`. Both live in `internal/session`, which holds no storage at all. The
implementation that ships is `internal/session/filestore`, behind
`filestore.New(dir)`, and a second one is a sibling package: `pgstore`,
`sqlitestore`. The concrete type is unexported — `New` hands back the contract,
since a caller that could name the type would be back where it started.

The split is a package boundary rather than a file boundary because a file
boundary is only a convention: in one package nothing stops `session.go`
importing the disk tomorrow. The import graph is the proof now —
`internal/session` pulls in neither `encoding/json` nor `path/filepath`, and
`go list -deps ./internal/session` does not mention `filestore`. It points one
way and cannot point back.

That forced `Pick`, the prefix rule, to be exported: `filestore` lives outside
the package and needs it, and so will the next backend. Which is the argument
for the shape — the rule is shared rather than reimplemented, and `Load` means
the same thing in every store. `NewID` is exported for the same reason: a store
handed a `Session` with no id has to name it. `orchestrator` names
the store in one line and hands it to `cli.UseStore`; nothing downstream ever
learns which it got. A second backend replaces that line and changes nothing
else.

`Latest` is a package function taking a `Store` rather than a fourth method,
since every backend can answer it from `All`. One that could do better —
`ORDER BY updated DESC LIMIT 1` — should grow an optional interface the way
`provider.ModelLister` does, instead of making every implementation carry a
method most would fake. `pick`, the prefix rule, sits outside the storage for
the same reason: `Load` means the same thing everywhere, and a backend
reinventing what a prefix is would be a bug nobody could see. Export it when
a backend lives outside this package.

The argument against building this was that a Go package is already a seam —
nothing outside `internal/session` knew a session was a file, so swapping the
insides for sqlite would have changed no caller either way. What the interface
buys over that is two implementations at once, and the honest version of it
has two: `TestStoreContract` runs the same scenario against `Files` and
against an in-memory store, so the contract is checked rather than asserted.
A backend added later is finished when that test passes for it. That test is
`package session_test` — an external test package, which is what lets it import
`filestore` without `session` importing it back.

`cli` keeps the live one in a package-level `current`, renamed from `session`
when the package took that name.

All three ways of asking save: the terminal after every turn, the pipe after
every line, and `-p` after its one answer. `-p` did not, for a while, and not
by decision — it answers from `orchestrator.RunOnce`, which bypasses `cli`
entirely and so never met `saveSession`. An answer that cannot be resumed and
does not appear in `-sessions` is one nobody can prove happened. The function
is `cli.SaveSession` now, for the same reason `cli.ContinueSession` is
exported.

A failed turn is saved too, everywhere. The tokens were paid for, and the
history `closeTurn` leaves behind is exactly what a `-resume` carries on from.

Two things `-resume` does that took a bug to learn:

- **It brings back the model.** `Session.Model` was written and never read, so
  a conversation held on Sonnet quietly carried on with whatever
  `settings.json` said today — a different window and a different price, with
  nothing on screen saying so. When that model can no longer be built, the
  fallback is a line on stderr rather than a silent swap.
- **It keeps writing to the same file.** Resuming used to copy the history into
  a new session, so an id nobody could hold on to grew a new twin every time.
  `cli.ContinueSession` hands the id and the start time back.

An id is matched against what was found rather than pasted into a path, so it
cannot be used to read a file elsewhere. Sessions saved before ids existed
still resume: the id comes from the file name, and `updated` falls back to
`started`.

The neutral types carry `json` tags spelling their own field names —
`provider.Message` and `provider.ContentBlock` are what a session file is made
of, so those two are the file format whether or not anyone meant them to be.
Untagged, renaming `ToolUseID` would have changed what is written without
touching a line of storage code, and every saved conversation would have come
back with its tool calls empty and nothing saying why. The tags are capitalised
because that is what was already on disk; snake_case is a migration rather than
a tidy-up, since Go's case-insensitive key matching rescues `Role` but never
`ToolUseID`. `TestSavedSessionKeepsItsWireNames` fails if the format moves.

### Credentials

`auth.json` maps a provider to a *map* of fields rather than to a key, because
one key is not always the whole story: an Anthropic key linked to an identity
can act in several workspaces, and the API answers 400 until the request names
one. `providerInfo.Extra` lists what `/connect` asks for after the key, one
question at a time, and `Save` merges rather than replaces — answering the
second question must not erase the first.

The field was declared with its env var months before anything read it, which
is the failure worth remembering: the store knew about workspaces, the client
never sent the header, and the error the API returned named a thing the code
already had a constant for.

## The model registry

`config/models.go` answers four questions about a model, and each is answered
because something asks it — a fifth, whether the model reads images, was
removed once it turned out nothing did: the picker was printing "images" beside
models uhai has no way to send an image to, which is worse than saying nothing: how much history fits before `agent` compacts, how
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
wrong low costs an early compaction, being wrong high costs the turn. Add a
family when a model behaves oddly, not because the table looks short.

The window follows the model, not the session, so `/model` and `/connect` move
it (`useProvider` in tea.go).

`/model` checks a typed name against the provider's own list — afterwards, in
the background, and only as a note. Not before, because a list is not the
truth: Z.ai answers `/models` with ten paid models and none of the free ones,
which work perfectly well, so refusing what a vendor forgot to list would block
a working model in order to catch a typo. The typo still gets a line, which is
better than what it used to get: a failed turn one prompt later, with an error
about the model that never mentions the spelling.

Two things keep the note from crying wolf, and both were bugs first:

- **A prefix counts as listed.** An alias is never in the list itself, only
  the dated id it points at, so the exact match warned about
  `anthropic/claude-sonnet-5` — the name uhai recommends and ships as its own
  default. A note that fires on the happy path is a note nobody reads.
- **`Models()` drops only what cannot hold a conversation.** It is two things
  at once — the picker's offer and what this check compares against — so
  anything filtered out is reported as a model the provider never heard of.
  Lacking tool support is not such a reason (uhai runs those with `UseTools`
  off), and neither is a small window or Google's `deprecated`, which is set
  months before a model stops answering.

A name chosen *from* the picker is not checked at all: it came out of that same
list a moment earlier, so the round-trip could only confirm a match it cannot
fail.

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

A task that fails keeps what it wrote. It reports nothing — that is what
failing means — so `/tasks t1` falls back to the tail of what it had written,
and `spawn_task` hands the model the same thing under the error. A review that
ran five minutes and died on a rate limit had already found things; throwing
that away and saying only "failed" wastes the work twice, once in tokens and
once in the asking again.

What `/check` runs is the project's own business: `check` in its
`.uhai/settings.json`, or a guess from the files present (go.mod, package.json,
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

A model that thinks shows its working out live, dimmed, above the answer, and
the moment the answer starts it collapses to `✻ thought for 12s`. Both halves
are the point: keeping all of it buries the reply, since a thinking model
writes more working out than reply; dropping it silently leaves the wait
unexplained.

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

`UHAI.md`, `AGENTS.md` or `CLAUDE.md` — first one found, each checked in a
`.local` variant first — is read into the system prompt every session, with
`@path` lines replaced by the file they name, three deep and cycle-guarded. The
`.local` files and the `@` imports are conventions rather than anything the
AGENTS.md standard defines; they are supported because projects use them. Skills are the other half: a folder
per skill with a `SKILL.md` under `.uhai/skills/` or `.claude/skills/`, of
which only the name and description reach the prompt. The body is a path the
model reads when the work calls for it, which is what keeps a project's fifty
pages of procedure from costing anything on a turn that does not need them.

Both layouts are Claude Code's, deliberately: a repository already written for
one agent should need nothing added for this one. That holds for personal
skills too: `~/.uhai/skills` and `~/.claude/skills` are searched as well, so
the ones you carry between projects arrive without being copied in. They are
searched *after* the project's, because `Skills` keeps the first name it finds
and the repository is the more specific answer — your own `rilis` gives way to
the one this project ships. There is no `~/.agents/skills`; that convention is
per-repository.

`/skills` opens the list, and enter switches the one under the cursor off or
back on. The list stays open, because switching one off is rarely the only one,
and its cost changes in place as it happens — deciding to switch a skill off
means looking at what it costs first, and those are the same rows.

It writes `"skillsOff": ["gaya"]` to the *project's* `.uhai/settings.json`,
which is the granularity the choice has: a skill you carry everywhere is wanted
in some repositories and not others. Switching one off everywhere is still a
hand edit of `~/.uhai/settings.json`. The list
accumulates the way a permission denial does — your own settings and the
project's are appended, so a project can switch off one of yours and cannot
switch on what you turned off for yourself. It stays in the list marked `off`:
switched off and never found look identical from the outside, and only one of
them is a mistake. Off accumulates, so a project cannot switch on what your own
settings turned off — the picker says so rather than appearing to do nothing.

Not built: switching one off for a session only, the way `a` works for
permissions. The reason to switch a skill off is that you do not want to pay
for it in this project, which is a durable fact and belongs in a file. *Build
it when somebody is toggling one twice in an afternoon.*

`/skills` prints what was found and where it looked. A skill in the wrong
folder, or whose frontmatter did not parse, fails in the one way nothing
reports — the model simply does not follow it, and no error names a cause. It
answers in a pipe as well, unlike every command except `/help`: putting the
only way to check discovery behind a terminal is putting it where a script
cannot look. `config.SkillDirs` is exported for it, and `Skills` searches
exactly that list, so the two cannot disagree about where it looked.

## Not built, on purpose

Each of these was considered, argued for, and left out. The trigger matters
more than the verdict: build it when the trigger fires, not because the list
looks short.

- **Typed agents** (`.uhai/agents/*.md`: a name, a prompt, its own tools and
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
