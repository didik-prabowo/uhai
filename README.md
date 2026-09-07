# uhai

A coding agent that lives in the terminal. It reads and writes files, runs
commands, and asks before doing anything it cannot take back.

It talks to whichever model you point it at — Anthropic, Gemini, OpenAI, Z.ai,
OpenRouter, or Ollama on your own machine — and the whole of it is about 4,800
lines of Go, small enough to read in an afternoon.

## Getting started

```sh
go install ./cmd/uhai   # onto your PATH, to use anywhere
uhai
```

Or `go build -o uhai ./cmd/uhai` and run `./uhai`, if you would rather keep
it here. Either way the binary is a copy: after changing the source, install or
build again. The welcome box prints the time the running binary was built,
which is the quickest way to catch having forgotten.

It opens without a provider, so the first thing to type is `/connect`, which
asks for a key and remembers it in `~/.uhai/auth.json`. `/disconnect` forgets one
again.

`uhai -attach` talks to a conversation running in a background daemon rather
than starting one in the terminal, so the work survives the window closing.
`/bg` starts that daemon on its own; `uhai -daemon` runs one in the foreground
to watch what it does, and `uhai -daemon-stop` stops it — which is what to do
after an upgrade leaves an older one still running.

One daemon serves every project, and every request names which. It stops on its
own after half an hour with nothing running and nobody attached. If an API key is
already in your environment — `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
`GEMINI_API_KEY`, `OPENROUTER_API_KEY`, `ZAI_API_KEY` — it is
used as it is, and there is nothing to connect.

Ollama needs no key at all: `/connect ollama`, once it is running locally.

Z.ai's GLM models are `/connect zai`, then `/model zai/glm-4.7`. Pay-as-you-go
needs credit on the account; the subscription "coding plan" is the same API and
key at another address, and is reached by pointing that one provider at it:

```json
{ "baseUrls": { "zai": "https://api.z.ai/api/coding/paas/v4" } }
```

The free `-flash` models work without any balance but are missing from the
list the API returns, so the picker cannot show them. Name one directly:
`/model zai/glm-4.7-flash`. They call tools like the paid ones do.

When a key stops working — expired, revoked, out of credit — `/connect <name>`
again and paste a new one; escape keeps the one already saved. If the key comes
from your environment it wins over anything saved, and the prompt says so
rather than letting you wonder why nothing changed.

For a script or a pipe there is no UI:

```sh
uhai -p "what does cmd/uhai do?"          # answer one prompt and exit
uhai -p "run the tests and fix the build" -y   # ...and let it write and run things
echo "sebutkan dua warna" | uhai            # a prompt per line
uhai -resume                                # carry on from the last conversation
uhai -sessions                              # what can be carried on
uhai -resume 2026-09-03T14-05             # ...carry on with that one
```

On the way out uhai prints the line that brings the conversation back, which
is the moment the id is worth having. A resumed conversation comes back with
the model it was held with, and keeps writing to the same file — its id survives being picked up and put down. Any
prefix of an id that names only one session is enough to type.

## Using it in a project

Run it from the root of whatever you are working on: every tool works relative
to the working directory, and so does everything it reads about the project.

Credentials and the chosen model are yours, not the project's — they live in
`~/.uhai/` and follow you everywhere. A project can override what it needs to
in a `.uhai/settings.json` of its own, and state its conventions once in an
`AGENTS.md` or `UHAI.md`, which is read at the start of every session.

```sh
cd ~/code/some-project
uhai
```

## Commands

|                            |                                                                     |
| -------------------------- | ------------------------------------------------------------------- |
| `/connect`                 | connect a provider and save the key                                 |
| `/model`                   | pick a model — the list shows context size, images, price           |
| `/compact`                 | summarize the history to free up context                            |
| `/check`                   | run the project's tests as a background task, or `/check <command>` |
| `/bg`                      | run a prompt in the background, read-only                           |
| `/tasks`                   | list background work, or `/tasks t1` to read one                    |
| `/stop`                    | stop a task: `/stop t1`                                             |
| `/mouse`                   | hand the mouse back to the terminal, for selecting text             |
| `/clear`, `/help`, `/exit` | as they sound                                                       |

## Keys

|                                       |                                                             |
| ------------------------------------- | ----------------------------------------------------------- |
| `enter`                               | send — or hold the prompt until the model is free           |
| `↑` `↓`, `1`-`3`                      | answer a permission question; `y`, `a`, `n` still work      |
| `esc`                                 | stop the model, or the compaction, mid-flight               |
| `↑` `↓`, `pgup` `pgdn`, `^p` `^n`     | the last five prompts                                       |
| `^y` `^e`, `shift+↑` `shift+↓`, wheel | scroll the chat                                             |
| `shift+drag`                          | select text (in tmux, `prefix + [` selects inside the pane) |

## Settings

`~/.uhai/settings.json` holds what you choose; a `.uhai/settings.json` beside
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
  listed. It applies to whichever provider is loaded, so use **baseUrls** —
  a map of provider to address — to move one and leave the rest alone.
- **check** — how this project verifies itself, for `/check`. Guessed from
  `go.mod`, `package.json` or a `Makefile` when absent.
- **permissions** — what each tool may do without asking; see
  [`docs/permissions.md`](docs/permissions.md).

Environment wins over both: `UHAI_MODEL`, `UHAI_BASE_URL`, `UHAI_API_KEY`.

An Anthropic key that is linked to an identity rather than to one workspace has
to say which workspace it acts in, so `/connect anthropic` asks for a workspace
id after the key — press enter to skip it, since an ordinary key carries its
own. `ANTHROPIC_WORKSPACE_ID` sets it from the environment.

Conversations are written to `~/.uhai/sessions` after every turn, one file per
session named by its id, which is what `-resume` picks up. `-sessions` lists
them: id, when it was last touched, the model, how much was said, and the first
thing that was asked.

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

Each tool is `allow`, `ask` or `deny` in `.uhai/settings.json`, so a project
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
what is refused and why — [`docs/roadmap.md`](docs/roadmap.md) has what is
built and what each unbuilt thing is waiting for, and
[`docs/permissions.md`](docs/permissions.md) has
the rules. Tests hold both pages to the code, so a tool nobody documented fails
the build.

## Telling it about your project

A file named `UHAI.md`, `AGENTS.md` or `CLAUDE.md` in the working directory is
read at the start of every session, so a repository can state its own
conventions once instead of you repeating them. This one has an `AGENTS.md`,
which doubles as the map of the code.

Each of those names also has a `.local` variant — `AGENTS.local.md` and so on —
which is read first: an untracked file for how you work, as opposed to what the
team agreed. A line that is only `@some/file.md` pulls that file in, so a
personal file can import the shared one. Neither is part of the AGENTS.md
standard; both are what projects already do.

Longer instructions for particular jobs go in skills — a folder per skill with
a `SKILL.md` inside, under `.uhai/skills/` or `.claude/skills/`, which is the
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

A project that keeps them elsewhere says where in its settings:

```json
{ "skills": ["local-docs/skills"] }
```

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

<!-- - crush — untuk UI. Dan untuk melihat bagaimana permission, skills, session, lsp dipisah; mereka juga punya hooks dan lsp yang belum ada di uhai.
- zot — untuk pembanding yang sepadan ukuran. Yang lain semuanya jauh lebih besar; zot mengaku "lightweight harness", jadi perbandingannya paling adil.
- zero — bukan untuk ditiru, tapi sebagai peringatan. Tujuh puluh package di internal/, termasuk terminalpet. Itu ujung lain dari spektrum yang uhai duduki. -->

## License

MIT — see [LICENSE](LICENSE). The same as bubbletea, lipgloss, glamour and
chroma, which uhai is built on, so nothing here is more restricted than what it
stands on.
