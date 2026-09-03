# ouhai

A coding agent that lives in the terminal. It reads and writes files, runs
commands, and asks before doing anything it cannot take back.

It talks to whichever model you point it at — Anthropic, OpenAI, Groq, Gemini,
OpenRouter, or Ollama on your own machine — and the whole of it is about 4,800
lines of Go, small enough to read in an afternoon.

## Getting started

```sh
go install ./cmd/ouhai   # onto your PATH, to use anywhere
ouhai
```

Or `go build -o ouhai ./cmd/ouhai` and run `./ouhai`, if you would rather keep
it here. Either way the binary is a copy: after changing the source, install or
build again. The welcome box prints the time the running binary was built,
which is the quickest way to catch having forgotten.

It opens without a provider, so the first thing to type is `/connect`, which
asks for a key and remembers it in `~/.ouhai/auth.json`. If an API key is
already in your environment — `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
`GROQ_API_KEY`, `GEMINI_API_KEY`, `OPENROUTER_API_KEY` — it is used as it is,
and there is nothing to connect.

Ollama needs no key at all: `/connect ollama`, once it is running locally.

When a key stops working — expired, revoked, out of credit — `/connect <name>`
again and paste a new one; escape keeps the one already saved. If the key comes
from your environment it wins over anything saved, and the prompt says so
rather than letting you wonder why nothing changed.

For a script or a pipe there is no UI:

```sh
ouhai -p "what does cmd/ouhai do?"          # answer one prompt and exit
ouhai -p "run the tests and fix the build" -y   # ...and let it write and run things
echo "sebutkan dua warna" | ouhai            # a prompt per line
ouhai -resume                                # carry on from the last conversation
```

## Using it in a project

Run it from the root of whatever you are working on: every tool works relative
to the working directory, and so does everything it reads about the project.

Credentials and the chosen model are yours, not the project's — they live in
`~/.ouhai/` and follow you everywhere. A project can override what it needs to
in a `.ouhai/settings.json` of its own, and state its conventions once in an
`AGENTS.md` or `OUHAI.md`, which is read at the start of every session.

```sh
cd ~/code/some-project
ouhai
```

## Commands

| | |
|---|---|
| `/connect` | connect a provider and save the key |
| `/model` | pick a model — the list shows context size, images, price |
| `/compact` | summarize the history to free up context |
| `/check` | run the project's tests as a background task, or `/check <command>` |
| `/bg` | run a prompt in the background, read-only |
| `/tasks` | list background work, or `/tasks t1` to read one |
| `/stop` | stop a task: `/stop t1` |
| `/mouse` | hand the mouse back to the terminal, for selecting text |
| `/clear`, `/help`, `/exit` | as they sound |

## Keys

| | |
|---|---|
| `enter` | send — or hold the prompt until the model is free |
| `↑` `↓`, `1`-`3` | answer a permission question; `y`, `a`, `n` still work |
| `esc` | stop the model, or the compaction, mid-flight |
| `↑` `↓`, `pgup` `pgdn`, `^p` `^n` | the last five prompts |
| `^y` `^e`, `shift+↑` `shift+↓`, wheel | scroll the chat |
| `shift+drag` | select text (in tmux, `prefix + [` selects inside the pane) |

## Settings

`~/.ouhai/settings.json` holds what you choose; a `.ouhai/settings.json` beside
the code holds what the project needs, and wins.

```json
{
  "model": "anthropic/claude-sonnet-5",
  "baseUrl": "http://localhost:1234/v1",
  "check": "CGO_ENABLED=0 go test ./...",
  "allow": ["go test", "git status"]
}
```

- **model** — `provider/model`. `/model` writes it for you.
- **baseUrl** — any OpenAI-compatible endpoint, for a provider that is not
  listed.
- **check** — how this project verifies itself, for `/check`. Guessed from
  `go.mod`, `package.json` or a `Makefile` when absent.
- **permissions** — what each tool may do without asking; see
  [`docs/permissions.md`](docs/permissions.md).

Environment wins over both: `OUHAI_MODEL`, `OUHAI_BASE_URL`, `OUHAI_API_KEY`.

Conversations are written to `~/.ouhai/sessions` after every turn, which is
what `-resume` picks up.

## What it can do to your machine

Six tools: read a file, write one, edit part of one, find files by name, search
their contents, and run a shell command. Writing, editing and running ask first
— the question shows the path and a diff of what changes, or the command
itself, so there is something to judge rather than something to trust. `a`
allows that tool for the rest of the session; `allow` in the settings makes the
answer permanent for the commands you name.

A seventh, `spawn_task`, lets the model hand a self-contained job to a fresh
agent and get back only its report, which keeps a long search out of the
conversation.

Each tool is `allow`, `ask` or `deny` in `.ouhai/settings.json`, so a project
can hand out less than the default — a session that reads a repository and says
what is wrong with it, but cannot touch it:

```json
{ "permissions": { "deny": ["Write", "Edit"], "ask": ["Bash"] } }
```

Shell rules are per command, and a chained line is judged part by part — so
allowing `git` does not quietly allow `git status && rm -rf /`:

```json
{ "permissions": { "allow": ["Bash(git:*)"], "deny": ["Bash(git push:*)"] } }
```

[`docs/tools.md`](docs/tools.md) has each tool in full — parameters, limits,
what is refused and why — and [`docs/permissions.md`](docs/permissions.md) has
the rules. Tests hold both pages to the code, so a tool nobody documented fails
the build.

## Telling it about your project

A file named `OUHAI.md`, `AGENTS.md` or `CLAUDE.md` in the working directory is
read at the start of every session, so a repository can state its own
conventions once instead of you repeating them. This one has an `AGENTS.md`,
which doubles as the map of the code.

Longer instructions for particular jobs go in skills — a folder per skill with
a `SKILL.md` inside, under `.ouhai/skills/` or `.claude/skills/`, which is the
layout Claude Code uses:

```
.claude/skills/rilis/SKILL.md
---
name: rilis
description: Publishing a version, from the tag to the release notes
---
The long part, which only gets read when it is needed.
```

Only the name and the description travel with every prompt. The body is a file
the model opens when the work turns out to be that work — so a project can keep
as many as it likes without paying for them each turn.

## Development

```sh
make check     # gofmt, vet, tests — what /check runs here
make run       # build it and open it
make install   # onto your PATH
make race      # the tests again, watching for data races
```

Behind each of those is one `go` command with `CGO_ENABLED=0` in front, which
is the whole reason the Makefile exists: without it the linker in a sandboxed
environment fails to build the test binaries, and says nothing useful about why.

`AGENTS.md` is the guide to the source — which front end runs when, why the
terminal behaves as it does, and which decisions were made deliberately and
should not be quietly undone.
