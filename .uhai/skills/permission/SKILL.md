---
name: permission
description: How uhai decides what needs asking: the three-list settings, why a tool names itself, and the holes that were closed.
---

# Permission

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
`walk.go` for what glob and grep share, with `ignore.go` beside it for what the
project itself says not to look at. It takes a context and checks it once
per entry: it is the one tool loop that can run for a long time without
touching the network or a subprocess, and cancellation used to stop at its
edge — a grep that finds nothing in a large tree read every file to the end
whatever the user pressed. The `.gitignore` at the search root is read once per walk: bare names,
`dir/`, `/anchored`, and the globs the matcher already understands. Not
negation, not one file per subdirectory, not the precedence between them —
that is a real implementation of a real specification. Being wrong about a
negation means a file is searched anyway, which is the direction to be wrong
in. `.git`, `node_modules` and `vendor` are skipped whatever the project
says, because a repository that tracks its vendor directory still does not
want grep reading it.

Rules are matched against the path relative to the search, not the absolute
one: `/uhai` is the built binary, and while they were matched absolutely it
also swallowed `cmd/uhai/`.

It also holds `searchTimeout`, fifteen seconds,
which is the bound `maxMatches` never was: that one stops a search finding
too much, and nothing stopped one finding too little in a tree too large.
What was found by then comes back with a note rather than as an error,
because half a large tree is usually already past where the answer was.
And it holds the pattern matcher, which
builds a regexp rather than calling `filepath.Match`, because `filepath.Match`
reads `**` as two stars that neither cross a separator and so matched exactly
one folder deep before going quiet — and `permission.go` — five lines saying which tools escape this process. In
`internal/cli`, `permission.go` answers "may this run, and how is it asked",
both halves in one place, because an answer is worth nothing if what it
approves cannot be read.

`docs/guide/tools.md` is the page for users, and `internal/tools/rules_test.go` holds
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

Tool names are constants in `tools/names.go`, and nothing else spells them.
They are the vocabulary three packages share — the schema sent to the provider,
the rules a person writes in `settings.json`, the questions the terminal asks —
and `"run_bash"` alone appeared thirteen times outside `internal/tools`.
Renaming a tool would have left every rule matching a name nothing answered to
and the confirmation showing raw JSON instead of a command, none of which fails
a build. `NameSpawnTask` is there too although the tool is not: it needs an
Agent, so it is built in `internal/agent`, but its *name* is shared and having
two spellings of it is what left `"deny": ["spawn_task"]` doing nothing.

A rule resolves through `config.toolName`, which knows a short list of friendly
spellings — `Bash`, `Read`, `Write` — and otherwise asks `tools.Definitions`.
A tool therefore answers to its own name without anyone maintaining a list,
which is the version of that map that could not have gone stale.

`permissions` in the settings is three lists of rules — `allow`, `ask`, `deny`
— in the shape Claude Code uses, `Bash(git push:*)` and `Read(*.env)`. Deny
beats ask beats allow, and the longest specifier wins, so ordering in the file
decides nothing. A tool denied outright is never offered to the model —
`agent.AllowTool` filters the list before it is sent — and refused if called
from memory anyway. A spawned agent inherits that function along with
`Confirm`: it used to inherit only the asking, which meant a background task
was handed tools the settings had refused. A denial is not a question asked
again in a window nobody is watching.

A rule naming a *path* is answered per call, and that is newer than it looks.
`config.Permission` is reached through one route — `decide`, in the terminal's
confirmation — and the agent asks that only when `tools.NeedsConfirm(name)`. For
`read_file`, `grep`, `glob`, `list_directory`, `find_symbol` and `set_plan` it is
false, so nothing ever asked config about them: `Read(*.env)`, the example three
paragraphs up, parsed correctly and matched a rule that was never consulted. A
deny that does not deny, failing in the way that names no cause — the file is
read, the model answers, nothing is logged. `AllowTool` takes the call's
arguments now, and answers a path rule for the tools that never reach a
confirmation; the ones that do are left to it, since that route shows a diff and
says *refused by this project's settings* where this one has only a blunter
sentence. `config.Subject` is the shared answer to "what is this call about", in
config rather than in the front end because two callers needed it and a second
copy is how one of them ends up ignoring a rule.

On top of that wiring, `read_file` refuses a file that holds a credential by
convention with nothing configured — `.env`, `*.pem`, `id_rsa*`, `credentials`,
everything under `.ssh/` — because a tool result goes four places: the model, the
history, the next request to a provider, and the session file in plaintext, and
reading was the one capability with nothing in front of it. A list of names, not
an inference: entropy detection false-positives on every hash, uuid and base64
blob in a repository, and reading repositories is the whole job. The templates
are excluded by name for the reason `$1` is excluded from `buildsItself` — a
default that fires on `.env.example` teaches people to override the category.
Refused rather than asked because a question needs a confirmation `read_file`
does not have, and `"allow": ["Read(./.env)"]` wins over it. The list lives in
`docs/guide/permissions.md` and a test fails if the page and the code part.
`run_bash` and `grep` are deliberately uncovered, and so is any secret whose name
is not conventional; that is the open half, and its issue holds the reasoning.

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
