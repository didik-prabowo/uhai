# Tools

Tools are what let the model do something rather than only say something: read
your files, search them, change them, run a command, read a page, search the
web. uhai has eleven, and the model is given exactly that list — there is no
hidden capability, and nothing reaches the network without you saying so.

Everything on this page is enforced in `internal/tools` and checked by
`internal/tools/rules_test.go`, which fails the build if the page and the code
disagree.

## Configure

Rules go in three lists — `allow`, `ask`, `deny` — each naming a tool and,
optionally, what it may act on:

```json
{
  "permissions": {
    "allow": ["Bash(go test:*)"],
    "deny":  ["Write", "Bash(rm:*)"]
  }
}
```

The defaults are that the tools which leave this process ask, and the three
that only look inside it do not:

| default | tools |
|---|---|
| `allow` | [`read_file`](#read_file), [`glob`](#glob), [`grep`](#grep), [`list_directory`](#list_directory), [`find_symbol`](#find_symbol), [`set_plan`](#set_plan) |
| `ask` | [`write_file`](#write_file), [`edit_file`](#edit_file), [`run_bash`](#run_bash), [`fetch_url`](#fetch_url), [`search_web`](#search_web) |

A chained command line is judged part by part, so allowing `Bash(git:*)` does
not quietly allow `git status && rm -rf /`.

[`docs/permissions.md`](permissions.md) has the rest: the rule syntax, what
wins when rules disagree, and what each answer at the prompt means.

## Built-in

### read_file

Reads a file and returns it as text — a window of it, when the file is longer
than one result can hold.

```json
{ "path": "internal/cli/tea.go" }
{ "path": "internal/cli/cli_test.go", "offset": 1200, "limit": 400 }
```

`offset` is the first line, counting from 1, and `limit` how many lines. Neither
is needed for an ordinary file: without them the read starts at the beginning and
returns as much as fits.

What it does when there is more says how to reach it:

```
…[lines 1-312, more follow — read on with offset 313]
```

That line is the whole point. A result cut at 8,000 characters with nothing to
say about it reads exactly like a file that ends there, and `cli_test.go` in this
repository is 107,890 bytes — the same 7% of it came back however many times it
was asked for.

An offset past the end is an error naming the file's length, since the useful
next move is a smaller offset. An empty file says it is empty. And a path that
cannot be read comes back as an error the model can act on, not as an empty
result — otherwise it carries on believing the file was empty.

### write_file

Creates a file, and any parent folders it needs, or overwrites what is there.

```json
{ "path": "docs/tools.md", "content": "# Tools\n..." }
```

The question shows a diff against the existing file, or the whole content as
added lines when the file is new. `{"permissions": {"deny": ["Write"]}}` for a
session that must not create anything.

### edit_file

Replaces one exact piece of text, including its indentation.

```json
{ "path": "main.go", "old": "\tprintln(\"halo\")", "new": "\tprintln(\"dunia\")" }
```

Three refusals, each preventing a particular way of quietly corrupting a file:

- `old` empty → *use write_file to create a file*.
- `old` not found → *read it again, it may have changed*. The file has moved on
  since the model last looked at it.
- `old` found more than once → *include surrounding lines to make it unique*.
  Replacing the wrong one of several matches is the worst way to be wrong,
  because nothing looks broken.

A refused edit changes nothing.

### glob

Lists files whose name matches a pattern.

```json
{ "pattern": "**/*_test.go", "path": "internal" }
```

`path` is relative to the project and defaults to the whole of it. A leading `**/` is dropped and the
rest matched against the file name, so `**/*.go` finds Go files at any depth.
There is no brace expansion and no `**` in the middle of a pattern —
`src/**/test/*.go` will not work. Use `grep`, or a command, for more than that.

### list_directory

Lists one folder: its files, and its subfolders with a trailing slash.

```json
{ "path": "internal/tools" }
```

One level, and `glob` for anything deeper. It exists because `glob` cannot
name a directory at all — the walk both searches share hands its visitor files
only, so the shape of a tree was reachable only through `run_bash ls`, which
asks permission for the least dangerous thing here.

`.gitignore` is read from the project root rather than from the folder being
listed, so listing a subfolder hides what listing the root hides. A folder
whose entries were all ignored says so rather than saying it is empty.

### grep

Searches file contents with a Go regular expression, returning
`path:line:text`.

```json
{ "pattern": "func Test\\w+", "path": "internal", "include": "*.go" }
```

Binary files are skipped, and so are unreadable ones: a corner you cannot open
is not a reason to fail the whole search.

### find_symbol

Asks the project's language server where a symbol is defined, who uses it, or
what implements it.

```json
{ "name": "UseProviderOn", "what": "references" }
```

It takes a name, not a line and column. The references this was read from take
a position, which means the model greps first and then counts characters into
a line — the step it gets wrong and cannot check. Every position uhai sends to
a server came from that server.

`what` is `definition` (the default), `references` or `implementations`. A name
declared in more than one place comes back as the list of places rather than a
guess; `path` picks one.

What it buys over `grep` is meaning. A method called `Run` collides with every
other `Run` in the tree and with the word in a comment; the language server
returns the ones that are the same `Run`.

A project with no language server installed — or written in a language uhai has
no entry for — gets a note saying so and suggesting `grep`, not an error. The
turn goes on.

Servers are started on first use and kept warm for the rest of the session,
because starting one costs an index of the whole project.

### set_plan

Writes down the plan for a job of several steps.

```json
{ "steps": [
  { "step": "read internal/tools", "status": "done" },
  { "step": "add the tool", "status": "doing" },
  { "step": "update the docs" }
] }
```

`status` is `todo` (the default), `doing` or `done`, and the whole plan is
sent every time — the newest call is the plan, not a patch to it.

It stores nothing. The plan is the tool result, and the tool result is in the
history the next request carries, so re-stating it is what keeps it alive.
That is why there is no plan on screen and nothing survives `/compact`: the
model is the only reader so far. A stored one can be built the day something
else needs to read it.

### run_bash

Runs one command through `bash -c` and returns stdout and stderr together, in
the order they were printed.

```json
{ "command": "go test ./..." }
{ "command": "go build ./...", "timeout": 420 }
```

Killed after two minutes, and killed as a whole process group — killing the
shell alone would leave `go test` compiling away with nothing to report to. A
command that waits for input dies rather than hanging the turn, so there is no
`vim`, no `git rebase -i`, no password prompt.

`timeout` asks for longer, in seconds, up to ten minutes — the same ceiling
`/check` has, since the limit was never about duration. Anything above is
clamped rather than refused. The confirmation says how long it is agreeing to
whenever it is not the usual two minutes, because the turn waits with nothing on
the screen for the whole of it: `/check` is still the better answer for a suite
that takes that long, since it runs in the background and can be stopped.

The command inherits your environment, less `UHAI_API_KEY` and `BRAVE_API_KEY`,
and is told there is no terminal — `NO_COLOR`, `TERM=dumb`, a pager that is
`cat`. Output containing a NUL byte is refused with a byte count rather than
returned as mojibake.

## Tasks

`spawn_task` is added by the agent rather than by this package: it hands a
self-contained job to a fresh agent and returns only that agent's report, which
keeps a long search out of the conversation. A task is refused anything that
writes or runs commands, since nobody is watching to answer for it, and a task
is not offered `spawn_task` in turn.

`/bg` runs a prompt the same way, and `/check` runs a command — see the README
for both.

## Internals

Searches walk the project themselves rather than shelling out, so
they need no permission and behave the same everywhere.

| | |
|---|---|
| result size | 8,000 characters, then `...[output truncated]` |
| `read_file` | 7,488 characters a time, then the offset to carry on from |
| `glob`, `grep` matches | 200, then the search stops |
| `grep` line length | 200 characters, then `…` |
| `run_bash` time | 2 minutes, or what `timeout` asks for, up to 10 |
| `run_bash` output | the last 7,488 characters, headed by how many were dropped |
| never walked | `.git`, `node_modules`, `vendor` |
| never searched | files that are credentials by convention, with the count said |
| never returned | credentials whose shape is recognisable, replaced by a marker naming the kind |

`run_bash` keeps the **end** of what a command printed, which is where a failure
explains itself, and says how many bytes went before it. Every other tool is cut
at 8,000 characters from the front, which is the right half of a file and the
wrong half of a test run — hence the difference.

### fetch_url

Fetches a page over http or https and returns it as text — documentation, a
changelog, an API reference the answer depends on.

```json
{ "url": "https://pkg.go.dev/net/http" }
```

It asks first, which is not what "reading" usually costs. The direction is why:
it reads by *sending*, the URL goes to somebody else's server, and a URL can
carry whatever the model decides to put in it. Left unasked it would have been
the way around `run_bash`'s confirmation — the same egress, through the tool
that does not stop. Allow it in `settings.json` if the asking is not worth it:

```json
{ "permissions": { "allow": ["Fetch"] } }
```

Only the public internet is reachable. Loopback, the private ranges and the
link-local block are refused, before the request and again on every redirect —
`169.254.169.254` is one address away from a cloud machine's credentials, and
an open redirect on a public host walks there in one hop. `file://` is refused
too: it would be a second `read_file` with none of its rules.

Markup is stripped rather than parsed — scripts and styles go whole, tags go,
entities come back as characters. Enough for prose; not for structure.

### search_web

Searches the web and returns titles, addresses and short descriptions.

```json
{ "query": "go 1.25 release notes", "count": 5 }
```

It returns descriptions, not pages: `fetch_url` is still what opens the one
that looked right. `count` is 1 to 20 and defaults to 5.

It asks first, and one step earlier than `fetch_url` does. The query itself
leaves the machine, and what somebody is searching for is often more telling
than the page they end up reading — the model writes that query out of
whatever is in the conversation.

**It needs a key.** Every keyless source was tried and none of them answer:
DuckDuckGo does not reply to this machine at all, Mojeek returns a captcha. A
free Brave key from <https://brave.com/search/api/> goes in `~/.uhai/auth.json`:

```json
{ "brave": { "key": "BSA..." } }
```

or in `BRAVE_API_KEY`, which wins over the file. `UHAI_API_KEY` deliberately
does **not** apply here, unlike everywhere else: it is a wildcard that fills
in every provider's key, and here it would send the key paying for the
conversation to a search engine that never asked for one.

No key is an answer, not an error — a note saying so, and the turn goes on.
That is the ordinary state of most machines and it must not cost a turn to
find out.
