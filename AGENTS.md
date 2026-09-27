# uhai

A CLI coding agent. `cmd/uhai` is the entry point and holds nothing but flag
parsing; everything else lives in `internal/`.

These are the things the code cannot tell you: what was decided, what was tried
and rejected, and what is deliberately unfinished. Everything else — structure,
naming, how a function works — read from the source, which is commented for it.

## Running and testing

    CGO_ENABLED=0 go test ./...

Go 1.25 or newer, which the Charm v2 packages require. `go.mod` asks for it and
`GOTOOLCHAIN=auto` — the default — fetches it, so no toolchain has to be
installed by hand.

The `CGO_ENABLED=0` is not optional here: the sandboxed linker cannot build cgo
test binaries, and the failure it produces says nothing about your change.

`go build -o uhai ./cmd/uhai` after touching the UI, then restart the running
session — a session started before the build behaves like the build it was
started with. The welcome box prints the binary's build time so that mistake
takes one glance to spot.

## Two front ends, and which one runs

`app.go:Run` looks at stdin and picks:

- `tea.go` — a bubbletea program on the alternate screen, for a terminal.
  Charm v2 throughout (`charm.land/...`), which moved three things: the alt
  screen and the mouse are properties of the `View` rather than program options
  and commands, so `/mouse` sets a bool and the next frame carries it;
  `lipgloss.AdaptiveColor` is gone in favour of `LightDark(isDark)`, so the
  palette is resolved once by `useTheme` when the terminal answers
  `BackgroundColorMsg` rather than at every use; and lipgloss now renders its
  colours whether or not a terminal is attached, where v1 rendered plain
  without one — which is why several tests compare `plain(…)` rather than the
  bytes. It
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

Two providers ship with an endpoint, `anthropic` and `openai`, and everything
else is `/connect` → `+ custom endpoint`: a name, a base URL, a key. Gemini,
Z.ai, OpenRouter and Ollama were in that table and are not any more — they all
speak the OpenAI format, so a table entry bought a default URL and a price
bracket and cost a line that had to stay true. `Known` means "uhai ships an
endpoint for this" and gates whether a price may be quoted; `Configured` means
"Known, or a baseUrl says where it lives" and gates the pickers. A gateway must
never become `Known`.

`commands.go` holds what both do — the command list, the providers, the session
on disk, the background tasks — with no printing in it, so neither front end
has to know how the other says things. `text.go` measures and cuts text for
either.

There used to be a third: a renderer that painted the terminal by hand, from
before bubbletea. It was deleted once the new prompt could do everything it
did. It is not in this history — the branch that built all this was squashed
into one commit — but the tag `backup/before-squash-1146` still points at those
74 commits, so a decision can be checked even though `git log` here cannot show
it.

## Where the rest is

The subsystems each keep their own notes under `.uhai/skills/`, read when
they are the thing being worked on rather than on every turn. They were in
this file until they were three quarters of it — 11k tokens on a request
that usually needed none of them.

- `permission` — permission
- `sessions` — sessions
- `providers` — providers
- `model-registry` — the model registry
- `tasks` — tasks
- `drawing` — drawing
- `daemon` — the daemon: the socket, workspaces, worker processes, and what it
  cost to have one

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

A 429 is also the one status read rather than counted, because several
failures wear it. Z.ai answers an empty wallet with 429 and "Insufficient
balance or no resource package"; OpenAI says insufficient_quota. Waiting does
not pay a bill, so uhai spent fifteen seconds failing three times identically
before saying so.

A quota counted per day is the third: Gemini's free tier allows twenty requests
a day and reports it with the same sentence it uses for a per-minute quota,
which waiting does fix. Only the quotaId in error.details tells them apart, so
that is what is read.

The body is peeked for the words that mean money — not the vendor's error code,
which every vendor numbers differently, and not "billing details", which was a
marker until it turned out to catch the recoverable Gemini case too — and then
put back, since the provider's own sentence is the only place the reason
appears.

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
- **A model per task.** Everything runs the session's model. Cheaper than it
  was: a task is its own process now, so this is an argument to `uhai -task`
  rather than a change to the agent. *Build it when the cost of background work
  is worth splitting — the status row prices each turn, so the moment will be
  visible.*

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
