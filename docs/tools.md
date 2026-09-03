# Tools

Tools are what let the model do something rather than only say something: read
your files, search them, change them, run a command. ouhai has six, and the
model is given exactly that list — there is no hidden capability, and no
network access beyond what a command you approved can reach.

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

The defaults are that the three tools which change something ask, and the three
that only look do not:

| default | tools |
|---|---|
| `allow` | [`read_file`](#read_file), [`glob`](#glob), [`grep`](#grep) |
| `ask` | [`write_file`](#write_file), [`edit_file`](#edit_file), [`run_bash`](#run_bash) |

A chained command line is judged part by part, so allowing `Bash(git:*)` does
not quietly allow `git status && rm -rf /`.

[`docs/permissions.md`](permissions.md) has the rest: the rule syntax, what
wins when rules disagree, and what each answer at the prompt means.

## Built-in

### read_file

Reads a file and returns it as text.

```json
{ "path": "internal/cli/tea.go" }
```

A path that cannot be read comes back as an error the model can act on, not as
an empty result — otherwise it carries on believing the file was empty.

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

`path` defaults to the working directory. A leading `**/` is dropped and the
rest matched against the file name, so `**/*.go` finds Go files at any depth.
There is no brace expansion and no `**` in the middle of a pattern —
`src/**/test/*.go` will not work. Use `grep`, or a command, for more than that.

### grep

Searches file contents with a Go regular expression, returning
`path:line:text`.

```json
{ "pattern": "func Test\\w+", "path": "internal", "include": "*.go" }
```

Binary files are skipped, and so are unreadable ones: a corner you cannot open
is not a reason to fail the whole search.

### run_bash

Runs one command through `bash -c` and returns stdout and stderr together, in
the order they were printed.

```json
{ "command": "go test ./..." }
```

Killed after two minutes, and killed as a whole process group — killing the
shell alone would leave `go test` compiling away with nothing to report to. A
command that waits for input dies rather than hanging the turn, so there is no
`vim`, no `git rebase -i`, no password prompt. Anything slower than two minutes
belongs in `/check`, which runs in the background and can be stopped.

## Tasks

`spawn_task` is added by the agent rather than by this package: it hands a
self-contained job to a fresh agent and returns only that agent's report, which
keeps a long search out of the conversation. A task is refused anything that
writes or runs commands, since nobody is watching to answer for it, and a task
is not offered `spawn_task` in turn.

`/bg` runs a prompt the same way, and `/check` runs a command — see the README
for both.

## Internals

Searches walk the working directory themselves rather than shelling out, so
they need no permission and behave the same everywhere.

| | |
|---|---|
| result size | 8,000 characters, then `...[output truncated]` |
| `glob`, `grep` matches | 200, then the search stops |
| `grep` line length | 200 characters, then `…` |
| `run_bash` | killed after 2 minutes |
| never walked | `.git`, `node_modules`, `vendor` |

A result is cut at the end, so what a command printed last — which is where a
failure explains itself — is what survives.
