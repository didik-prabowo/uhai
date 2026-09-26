# Everything it does

The README has the eight lines worth scanning. This is the rest, and most of
it is interesting only once you have hit the problem it solves — which is why
it is here and not there.

## Any model, and the one that actually answered

Two providers ship with an endpoint, `anthropic` and `openai`. Everything else
is `/connect` → `+ custom endpoint`: a name, a base URL, a key. That reaches
any OpenAI-compatible endpoint there is, because the client that talks to
OpenAI does not care who answers — a gateway, your company's, Ollama on
localhost.

Gemini, Z.ai, OpenRouter and Ollama shipped in that table once and do not any
more. All four speak the OpenAI format, so an entry bought a default URL and
a price bracket and cost a line of code that had to stay true. The
[README](../../README.md#anything-else-that-speaks-the-format) has their
endpoints for pasting.

`/model` changes the model in the middle of a conversation and keeps
everything said so far. What follows the model changes with it — the context
window, whether it thinks, how hard — while whether the conversation has tools
does not, because the history already commits to that and switching it off
midway would strand the tool calls already in it.

**Behind a router, the configured name may not be a model at all.** One is an
alias over five of them, picked per turn. uhai reads the model that replied
out of the response and sizes the turn from *that*. Before it did, a
conversation with a million-token model was being summarised away every 27k
tokens, against a 32k default the alias had fallen back to — and nothing on
screen said so, because from uhai's side nothing was wrong.

A gateway is sized but never priced. Knowing a model's context window is a
fact about the model; quoting a price is a claim about somebody's billing,
and a gateway called `cc` serving Opus was once billed at Anthropic's list
price for turns that cost nothing.

## Asking before it acts

Every tool is `allow`, `ask` or `deny`, in the project's `.uhai/settings.json`
or your own. The defaults are that the tools which leave this process ask and
the ones that only look inside it do not.

The question shows what will happen: a path and a diff for an edit, the
command itself for a shell call. `a` allows that tool for the rest of the
session; `allow` in the settings makes it permanent for the commands you name.

A chained shell line is judged part by part, so allowing `Bash(git:*)` does
not quietly allow `git status && rm -rf /`, and a command that builds itself
out of `$(...)` is never allowed silently.

`fetch_url` and `search_web` ask too, which is not what reading usually costs.
The direction is why: they read by *sending*. Loopback, the private ranges and
the link-local block are refused before the request and again on every
redirect — `169.254.169.254` is one address away from a cloud machine's
credentials, and an open redirect on a public host walks there in one hop.

[`permissions.md`](permissions.md) has the rule syntax and what wins when two
rules disagree. [`tools.md`](tools.md) has every tool in full.

## Asking the language server

`find_symbol` answers where a symbol is defined, who uses it, and what
implements it — by meaning, where `grep` answers by text. `grep -rnw Run` over
a codebase returns every `Run`, every `Run` in a comment, and the word in
prose; the language server returns the ones that are the same `Run`.

**It takes a name, not a position.** The implementations this was read from
take a line and a column, which means the model greps first and then counts
characters into a line — the step it gets wrong and cannot check. Every
position uhai sends to a server came from that server.

Four servers: gopls, typescript-language-server, pyright and rust-analyzer,
chosen by the markers at the project root. They start on first use and stay
warm, because starting one costs an index of the whole module. A project with
none installed gets a note saying so and suggesting `grep`, not an error — the
turn goes on, which matters because that is the ordinary state of most
machines.

Diagnostics are deliberately absent: they are what `go build ./...` already
answers, and the document-syncing they require is most of what a real LSP
client is.

## Work that outlives the terminal

A daemon holds the conversation, so closing the window does not end the job.
`uhai -attach` joins it from anywhere, `uhai -daemon` runs one in the
foreground to watch what it does, and `uhai -daemon-stop` stops it whatever it
is doing. Nothing has to be started by hand — `-attach` and `/bg` start one if
none is running.

One daemon serves every project, and every request names which. It stops on
its own after half an hour with nothing running and nobody attached. After an
upgrade a new front end asks the older daemon to stand down, unless it is
busy, in which case it is left alone and said so.

Background work runs in a process of its own rather than a goroutine, because
the failures worth surviving are the ones `recover` cannot catch: a concurrent
map write is fatal, not a panic, and it would have taken every project's
conversation with it.

- `/bg` runs a prompt in the background, read-only
- `/check` runs the project's tests, or `/check <command>`
- `/tasks` lists them, `/tasks t1` reads one, `/stop t1` ends one
- `spawn_task` is the model's own version: a self-contained job handed to a
  fresh agent, returning only its report, which keeps a long search out of the
  conversation

## Conversations that come back

Every conversation is a file under `~/.uhai/sessions`, named by the moment it
started. `-sessions` lists them and `-resume <id>` takes any prefix that names
only one — and brings back the model it was held with rather than whatever
`settings.json` says today.

When the window fills, the history compacts itself: the older half becomes a
summary and the conversation carries on, rather than the turn failing at the
point it got interesting.

**A turn that fails in the middle is closed rather than left open.** A rate
limit, a timeout, an escape — whatever the cause, the history would otherwise
end on a user message, which the Messages API refuses to follow with another.
The failure was silent and the *next* prompt was refused, naming a rule rather
than the turn that broke it. Now the turn ends with what was streamed plus one
line saying why it stopped.

Retrying is deliberately uneven. A 429 waits five seconds and then ten; a 5xx
one and two; a transport error is not retried at all, since the three minutes
it already waited are the evidence that waiting is not the answer. And a 429
that means an empty wallet rather than a busy minute is read rather than
counted — waiting does not pay a bill.

## Knowing your project

`UHAI.md`, `AGENTS.md` or `CLAUDE.md` — the first one found, each checked in a
`.local` variant first — is read into the system prompt every session, with
`@path` lines replaced by the file they name.

Skills are the other half: a folder per skill with a `SKILL.md` under
`.uhai/skills/` or `.claude/skills/`, of which **only the name and description
reach the prompt.** The body is a path the model reads when the work calls for
it, which is what keeps a project's fifty pages of procedure from costing
anything on a turn that does not need them. `~/.uhai/skills` and
`~/.claude/skills` are searched too, after the project's, so the ones you carry
between projects arrive without being copied in.

Both layouts are Claude Code's, deliberately: a repository already written for
one agent should need nothing added for this one.

`/skills` lists what was found and where it looked, and enter switches one off
— written to the project's settings, because a skill you carry everywhere is
wanted in some repositories and not others.

## Two front ends

Which one runs is decided by stdin. A terminal gets a full-screen program with
the prompt pinned to the bottom; a pipe gets a prompt per line on stdout.

```sh
uhai -p "what does cmd/uhai do?"     # answer one prompt and exit
echo "name two colours" | uhai       # a prompt per line
```

In a pipe nobody is there to answer a confirmation, so the tools that write or
run commands are refused rather than silently allowed. `-y` is the explicit
opt-out for `-p`.

## What the turn costs

The status row carries the tokens each way and, where the model is one whose
price is known, what the turn cost. The context window comes from a registry
of real figures rather than a family guess — two of them were wrong in the
direction that costs a turn before the registry was checked against what the
APIs actually list.

Anthropic's prompt cache is used where it exists, and every OpenAI-shaped
vendor's cached-token discount is read back out of the response rather than
the whole prefix being billed as fresh.

## What is deliberately not here

[`../roadmap.md`](../roadmap.md) ends with things that were considered and left
out, each with the trigger that would justify building it. The short version:
no MCP yet, no images, no plan drawn on screen, and Windows compiles but has
never been run.
