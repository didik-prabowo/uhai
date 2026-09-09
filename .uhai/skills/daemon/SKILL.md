---
name: daemon
description: The agent as a process the terminal can outlive — the socket, the second stream, and what has and has not moved onto it yet.
---

# Daemon

`uhai -daemon` runs a process that listens on `~/.uhai/daemon-<hash>.sock`,
one socket per project. It exists
for one thing that can be seen today: `/bg` starts a goroutine in the front
end's own process, so closing the tab kills the task mid-flight and loses its
report. Every other reason a daemon is usually built — an editor client, LSP,
two terminals on one conversation — is a reason to keep it, not the reason it
was started.

## What is on it, and what is not

`/bg` is on it. `POST /v1/tasks`, `GET /v1/tasks` and `POST /v1/tasks/{id}/stop`,
alongside `/v1/health`, `/v1/sessions` and `/v1/events`. A background task now
runs in the daemon and keeps going after the terminal that asked for it has
closed.

Background work went first for a reason that is not about size: the `/bg`
sub-agent already refuses anything needing confirmation, because nobody is
watching to answer. So it is the one part of the agent that needs no permission
round trip — the daemon can run it without first solving how a process with no
screen asks a human whether to write a file.

**The conversation is on it.** `POST /v1/prompt` runs one turn of the
conversation the daemon holds; what the answer looked like on the way is on the
event stream, so a front end subscribes first, posts second, and draws as it
goes — which is what it already did when the agent was a function call in its
own process. `uhai -attach` opens a terminal on it.

One turn at a time, under a mutex. A conversation is a single history, and two
turns writing to it at once interleave into something neither caller asked for.

The turn does not hold the front end's context. A terminal that hangs up
mid-answer has left the room, not cancelled the work: half a turn in the
history is worse than a whole one nobody watched.

Escape is the other decision and has its own route, `POST /v1/prompt/stop`.
Hanging up and pressing Escape look identical over a socket unless they are
told apart deliberately, and one of them means "I have gone" while the other
means "stop". It also releases a question still waiting for an answer: one
whose turn has been abandoned should not hold a terminal until its deadline.
Pressing it with nothing running answers 200 — a front end that pressed twice,
or pressed as the answer landed, has not made a mistake.

The permission round trip is what made it possible. `Server.Ask` is what a daemon hands the agent as its `Confirm`
hook: the question leaves as an event, the terminal answers with a POST to
`/v1/questions/{id}`, and `Ask` blocks in between. In one process that is a
function call returning a bool — this is the whole reason the move is a piece
of work rather than a move.

A question is held open for exactly as long as somebody could answer it. It
used to have a deadline of a minute, and that was the wrong shape: a minute is
what it takes to read a diff properly, so the tool was denied out from under
someone still deciding and the turn carried on as if they had said no.
`workspace.watched` is what the wait watches now, polled rather than
signalled — `watch()` would have to broadcast on unwatch and every open
question listen for it, which is machinery for a case where nothing needs the
answer within a second of the terminal closing. `answerWait` survives as the
grace *after* the last watcher goes, because an SSE connection that drops and
reconnects is not somebody leaving.

Silence is still a no, both ways: a cancelled context and a run-out grace both
deny. The agent's own default denies everything so a caller who forgets the
hook cannot silently write files, and a daemon nobody is watching is that same
situation from further away. A question is dropped when it is given up on, so
the next terminal does not trip over one nobody is waiting for — but an open
one is visible at `GET /v1/questions`, so a terminal reopened mid-turn can
still answer it.

Both registries number from `t1`, so a task the daemon holds is shown as `d1`
and `/stop d1` routes there. Without the prefix, `/stop t1` is ambiguous the
moment a session has one of each — which happens as soon as the model calls
spawn_task while a daemon is running.

## One daemon, every project

`uhai -daemon` listens on `~/.uhai/daemon.sock` — one per user, serving every
project on the machine. It was one socket per project for a while, which made a
cross-project mix-up impossible by construction. This is the other trade, and
the one crush and zero both make: one process, one log, and no daemon left
behind for every checkout ever opened.

What that gives up has to be paid back deliberately.

**Every request names its project**, in an `X-Uhai-Project` header. A header
rather than a path segment or a body field: it applies to GET and POST alike,
and a route that forgot it would have to forget it somewhere visible.

**A request that does not name one is refused.** Defaulting to anything — the
daemon's own directory, the last project seen — is exactly how the per-user
version answered project B with project A's instructions, silently. Health
refuses too; it was the one route that swallowed the error and answered 200
anyway, which made the guard look total while leaving a door open.

**Nothing a conversation needs lives on the Server.** The agent, its tasks, its
questions, its watchers and its turn lock are all fields of a `workspace`, one
per project, created the first time that project is heard from. Isolation
per-object rather than per-lookup: a leak would have to be written on purpose
rather than by forgetting a filter.

Task ids and question ids both start at `t1` and `q1` in every project, so the
tests name the collision directly — one project stopping another's `t1`, or
answering another's `q1`.

Measured against a live model, one daemon on one socket:

```
tugas latar   proyek A → "PROYEK A"      proyek B → "PROYEK B"
percakapan    terminal A melihat "PROYEK A", terminal B "PROYEK B"
              bocor lintas proyek: tidak ada
tanpa header  400 no X-Uhai-Project header
```

One daemon serving everything also means one crash costs everything, so a
background task runs in a **process** of its own — `uhai -task <prompt>`, spawned
with the project as its working directory.

It was a goroutine with a `recover` around it, and the comment there admitted
what that is worth: a recovered panic leaves whatever it corrupted corrupted.
The failures most worth surviving are the ones `recover` cannot catch at all. A
concurrent map write is a fatal runtime error rather than a panic, and it would
take the daemon and every project's conversation with it.

zero's answer is a supervisor over a worker pool. This is the cheap two thirds
of it, and the seam that made it cheap is `ws.run` — the `Runner` the
orchestrator hands the daemon. Nothing in `internal/daemon` changed. Nor was a
protocol needed: a task is one-shot, so stdout is the report, the exit code is
the status, and killing the process is how `/stop` already worked.

`RunTask` is not `RunOnce` with a flag. It never saves a session — a file per
`/bg` would bury the conversations — never confirms anything, and answers in a
shape meant for a program: one JSON line, last, so a model or a tool printing
to stdout cannot displace it. The token count travels in that line rather than
being parsed out of prose, which would break the first time the prose changed.

Measured, against a stub endpoint so no quota was spent:

```
task selesai   status done, tokens 549, report "dua warna: merah, biru"
worker mati    status failed, daemon answers health, pid unchanged
```

What is still zero's and not ours: a pool, restart policy, and workers for the
*conversation* as well as for tasks. The conversation is the stateful thing the
daemon exists to hold, and moving it out is a different feature — one that
needs the protocol this deliberately avoided. *Build it when a conversation
needs to survive the daemon rather than the terminal.*

crush routes the same thing through `/v1/workspaces/{id}/...` and registers
workspaces up front; here a project is remembered the first time it speaks, so
a machine with ten checkouts pays for the ones actually used.

## Leaving

A daemon that starts itself has to leave by itself, or a machine collects them
— which this one already did before there was a daemon at all: four orphaned
processes turned up from runs days earlier.

It stops after half an hour with nothing to do. Half an hour rather than
minutes because stopping is not free: the conversation each project holds lives
in that process, and a daemon that exits takes it with it. The turns are on
disk — every one is saved — but a daemon started again does not read them back,
so an attached terminal after an idle exit begins a fresh conversation.

Nothing that would be lost is: a task running or queued holds it open, a turn
in flight holds it open, and so does a terminal sitting at an idle prompt.

The conversation is no longer lost either. A workspace built for a project now
picks up that project's newest saved session — `session.LatestIn` for the
history, `cli.ContinueSessionIn` for its id, model and spend — so a terminal
attaching after an idle exit carries on where the morning left off rather than
meeting a daemon with no memory of it. It is only ever *that* project's
newest, which is what `-resume` with no id means, and for the same reason: a
conversation about another tree carried on here acts on names that are missing
or, worse, on different files with the same names.

Picking it up silently was the other half of the bug, and it survived the fix
for a while. The daemon remembered the morning; the terminal attaching to it
drew a blank screen, because the opening line counted `agent.History` and the
*local* agent has none when attached. So the first prompt after an idle exit
was answered out of a context nobody on that side could see — a wrong answer
with nothing on screen to explain it, which is the shape every bug in this
corner has had.

`Conversation.Resumed` and `ResumedTokens` are what was read back; health
carries them, and the attached terminal opens with a line naming them. They
are snapshots taken at build and never updated: it is a fact about how this
daemon started, not a running total, so it needs no lock and cannot be read
off an agent mid-turn. `/clear` is refused while attached for the matching
reason — it would empty a local agent that answers nothing, and appear to
work.

The messages themselves are still not drawn. The count is what makes the
context visible; replaying it needs a route, a version, and a translation from
`provider.Message` back to chat entries. *Build it when the count stops being
enough to remember what was said.*

### One project per turn, on disk as well

The workspace keys everything a conversation *holds*. What it **does** was
keyed by nothing: `launcher.go` starts the daemon with no `cmd.Dir`, so it
inherits the directory of whichever front end first needed one and keeps it for
life, and the tools resolved their relative paths against the process. Nothing
chdirs at turn time — `inRoot` in the orchestrator wraps agent *construction*,
which is why AGENTS.md and the permission lists were already right and the
tools were not.

So a turn in project B read, wrote and ran commands in project A's tree. It is
the socket-per-project bug again, one layer down, and it hid for the same
reason: with one project the inherited directory *is* the project, and every
path resolves to a file that exists.

`Agent.Root` is the project, `tools.Execute(ctx, root, …)` carries it to the
call, and `Shell` takes a directory. On the call rather than in a package
variable, because the daemon runs two projects' turns at once — each workspace
has its own `turning` lock, not a shared one — so anything stored would be read
by the wrong turn.

Two details worth keeping:

- **Absolute paths are left alone.** Confining them to the project is a policy,
  and uhai already has one in the allow/ask/deny lists; resolving here would
  quietly change what a rule someone wrote means.
- **Searches walk absolute and report relative.** The pattern the model writes
  is a project path, and so is every result it hands back to `read_file`.
  Reporting absolute paths would break the first and bloat the second.

### One session per project

That restore was blocked by something worse underneath it. `cli` kept a single
`current` session — right for a front end, which is one process on one project,
and wrong the moment the daemon began saving for several: both projects wrote
into the same id, so the second turn overwrote the first project's file, `root`
and all. Not mixed up — *gone*: `LatestIn` for that project then found nothing,
which is exactly what the restore would have read.

It is now one session per resolved root, behind a mutex because the daemon
saves from a request goroutine per project and two can finish at once.
`session.Resolve` is exported for the key, so the map agrees with `SameRoot`
about what one project is — `/tmp` and `/private/tmp` must not become two
conversations.

`spent` was the twin and went the same way. It is the conversation's bill, kept
per model, and as a package-level slice it belonged to whichever project spoke
last: loading project B's session replaced it wholesale, and project A's next
save wrote B's cost into A's file. It lives on the session now, where
`session.Spend` was already stored, and `saveSession` no longer copies a global
in — `recordUsageIn` put it there as the calls happened.

Which exposed a third thing, in the daemon rather than in `cli`: `conv.OnUsage`
published usage to the watching terminals and recorded it nowhere. A turn run
by the daemon showed its cost on screen and was saved with none against it, so
a conversation held entirely through the daemon resumed claiming it had cost
nothing. It bills as well as publishes now.
Watching counts on purpose. A terminal that has said nothing for an hour is
still the reason the daemon exists, and pulling the socket from under it would
leave it drawing a session connected to nothing — the failure the reconnect was
written for, and not one to cause deliberately.

## Versions

A daemon outlives the terminal that started it — that is the point of having
one — so after `go install` a week-old daemon is still holding the socket and
the new binary talks to it. `ProtoVersion` rides on `/v1/health`, which every
client calls first, so the check sits in one place and no route can be added
that forgets it.

A mismatch is `ErrWrongVersion`, told apart from "no daemon" deliberately: one
means start one, the other means the one that is there has to go first, and a
front end that confused them would start a second daemon that could not bind
the socket. A daemon too old to report a version at all answers 0, which is a
mismatch and reads as one.

`uhai -daemon-stop` is the fix, and it is a person's command rather than
something a front end does after an upgrade: one daemon serves every project,
so stopping it ends background work everywhere.

Bump `ProtoVersion` when a change would make an older front end misread a newer
daemon or the reverse. A new event kind an old client ignores is not that; a
changed meaning for an existing one is.

## When the daemon goes away

The event stream reconnects, with a backoff from 100ms to five seconds. A
daemon that restarts — upgraded, crashed, stopped by hand — used to leave every
attached terminal silent with nothing on screen to say so: the stream ended and
the front end went on drawing a session connected to nothing.

The *first* connection is not retried. Someone asking to watch a daemon that is
not there should be told now, not left holding a channel that may never produce
anything.

Events published while it was away are lost, and the reconnect says so out
loud. Keeping them would need the daemon to know who had been listening and how
far behind they were — a sequence number and a queue per client — and a notice
about the gap is the honest smaller answer.

## Attaching

`uhai -attach` draws the daemon's conversation instead of starting one here.
A mode, not a silent upgrade, because the two conversations are separate: the
daemon built its agent from config when it started, so `/model` and `/connect`
in an attached terminal would change this process and not the one answering.
Choosing quietly between them would leave a user unable to say which
conversation they were in, and a wrong guess costs a turn against the wrong
model. Attaching to a daemon that holds no conversation is an error rather than
a fallback, for the same reason.

So the model is the daemon's to report and the daemon's to change.
`/v1/health` carries the model its conversation answers with, and everything
that names one reads that instead of the local provider. It used to read the
local one: `/model` swapped a provider nothing asks, the status row read that
same provider, and the row named a model no turn had run against while the
answers kept coming back in the old model's voice. Nothing on screen
contradicted it.

`POST /v1/model` is the switch, taken under the turn lock so it waits for the
answer being written rather than changing the model halfway through it. The
daemon resolves the name and sends back what it resolved — a terminal that
echoed what was typed would print a model nobody had loaded — and saves it, so
the next daemon starts where this one was left. A name the provider refuses
comes back as an error and changes nothing: the alternative is a daemon with no
provider because a name was mistyped. Refusing it here was the first version,
and it lasted about ten minutes of real use.

### The rule the attached front end keeps breaking

Five fixes have now landed in this corner and every one is the same sentence:
**attaching moved where the agent runs, and some part of the front end went on
reading its own object.** The local `agent.Agent` in an attached terminal has
no history, no usage and a provider nothing asks. Reading it is never an error
the compiler can see, and never an error the screen shows — it produces a
plausible number.

What it looked like each time:

    the model row       named the local provider while the daemon answered
    permission          decided locally? no — it skipped the rules entirely
    the opening line    counted a local history that is always empty
    /clear, /compact    emptied a local agent that answers nothing
    the price           priced the daemon's tokens at the local model's rate
    ctx %               measured a local history that is always empty

So, before touching anything the front end draws or any command it answers:
ask whether the fact belongs to the conversation or to this process. If it is
the conversation's, it is the daemon's when attached — `attachedModel`,
`attachedHolding`, `pricingModel()`, `resumedHistory()` are where that lives —
and a command that would change it either goes over the socket or is refused
out loud. Never done quietly: a command that appears to work and changes
nothing is how every one of these read from the outside.

`/connect` is still refused while attached: it writes a credential, and the
settings it would have to be written into are the ones the daemon reads.

`Builder` returns a `Conversation` — the two runners, the model, and the setter
— rather than four values, of which two were strings nobody could tell apart at
the call site.

`daemonMsg` turns each event into the message the agent's own callback would
have produced, so the screen, the spinner and the cost row cannot tell which
side of the socket the answer came from. It is a function of its own because a
terminal is not something this repository's tests can drive — the translation
is the part that can be checked, and it is.

A question is the one event that is not a translation: it needs a reply channel
and a POST back, so it goes through the same confirmation the local agent uses
and the answer is posted in a goroutine. The pump has to keep draining, or a
second question would wait behind a human.

The *same* confirmation, which it was not: `askOnBehalfOfTheDaemon` went
straight to the panel and skipped `decide` entirely, so attaching quietly threw
away the project's permission rules and made "always" a button that remembered
nothing — the same tool was asked about on every single call. `settled` is that
decision, in `permission.go`, and both front ends go through it. Attaching
moved where the agent runs, not who decides.

## Starting it

`/bg` starts one when there is none, and only `/bg` does: reading a task list
is not a reason to leave a process behind on a machine that had none. The child
is detached with `Setsid`, so it reparents to init rather than dying with the
terminal — a daemon inside the terminal's session would be the same bug one
process further away.

Two things that had to be got right, both found by running it rather than
reading it:

**A second daemon refuses rather than clobbering.** `removeStale` dials before
it deletes. Without that, two front ends starting at once end with the loser
deleting the winner's socket: the winner keeps running, reachable by nobody,
and both believe they succeeded.

**A spawned process does not spawn again.** A binary that does not understand
`-daemon` runs its front end instead, which calls `Ensure`, which starts
another — a chain with no end. `UHAI_DAEMON_SPAWNED` marks the child so it
stops at one. Seen for real from a stand-in that lacked the flag.

The socket can be driven with curl:

```
curl -s --unix-socket ~/.uhai/daemon.sock http://uhai.local/v1/health
```

The host name is a fiction. `net/http` insists on a URL with a host and a unix
socket has none, so every request is addressed to `uhai.local` and the dialer
ignores it.

## The second stream

uhai already streamed before any of this: the provider sends `text/event-stream`
and the answer arrives while it is written. `/v1/events` is a *different*
stream, and it exists only because there are two processes — it carries the
answer the rest of the way, from the daemon to whatever is drawing it.

In a single process that job is done by `a.OnDelta`, a Go closure, for free. A
daemon does not make streaming possible; it makes a second stream necessary.

## Two things the socket has to get right

**It is a credential.** Anything that can open it can ask the agent to run a
command, so it is `0600` in the user's own directory, chmodded between `Listen`
and the first accept. Without that step it is `0644` — every user on the
machine gets a shell. There is a test that fails on exactly that.

**A slow front end must not stop the work.** `Publish` drops rather than
blocks: a client that has stopped draining is a client that has gone away, and
holding the agent for it would stop the very work the daemon exists to keep
running.

A unix socket is a file and outlives the process that made it, so the daemon
removes its own on the way out and clears a leftover one on the way in.

## Starting one, and the lock that stops two

`Ensure` starts a daemon when none is running — `os.Executable`, `-daemon`,
`Setsid`, `Release`, output to the socket's own log, then poll for readiness
until `startWait`. Same shape as crush's `spawnAndWaitReady`, and started
rather than required for the reason in its comment: a daemon nobody remembers
to run is a daemon that never runs.

Two callers reach it, and only two: `cli.Attach` and `/bg`. `daemonClient()`
deliberately does not — reading a task list is not a reason to leave a process
behind on a machine that had none. crush calls `ensureServer` on every client
start instead, because crush *is* a client-server program; uhai runs perfectly
well without one.

`lockSpawn` serialises the spawn on a `daemon.lock` beside the socket. Two
front ends reaching `Ensure` at the same moment already survived it — the loser
fails to bind, exits, and its client finds the winner on the next poll — but
through a failure written to the log rather than by design, and a log full of
"a daemon is already listening" teaches the next reader that something is wrong
when nothing is. Whoever takes the lock looks again before spawning: the daemon
it was about to start may be the one the previous holder just started.

Three decisions inside it, each with a cheaper alternative that is worse:

- **Non-blocking `flock` in a poll loop**, not a blocking one. A blocking call
  cannot be told about `ctx`, and a front end that cannot be interrupted while
  starting a daemon is worse than one that occasionally spawns twice.
- **A file beside the socket**, not the socket. Locking the socket would tie
  the right to *start* a daemon to a file the daemon deletes when it stops.
- **A lock that cannot be taken is not fatal.** The unsynchronised path is what
  this always did; being unable to create a file is a reason to be careful, not
  a reason to refuse to work. crush makes the same call.

### Standing down a stale one

After a `go install` the daemon holding the socket is last week's build, and it
holds the socket, so nothing newer can bind. `Ensure` asks it to stand down —
`POST /v1/shutdown?if_idle=1` — waits for the socket to go, then spawns.

Asked, not told. The trigger written here was "build it when the daemon can
stand down one workspace without stopping", and that turned out to be the wrong
shape: what protects another project's work is not per-workspace shutdown but
refusing to stop at all while any project has any. `busy()` already answered
exactly that question for `ReapWhenIdle`, so the whole feature is that call and
a 409.

A refusal is `ErrBusy` and leaves the old daemon running and this terminal
without one — which is what it did before it could ask. The wrapped error names
the mismatch either way, since nothing else reports it.

`Shutdown` without the query stays what it was: a person's command, which takes
every project's work with it. That is why it is `uhai -daemon-stop` and not
something a front end does on anyone's behalf.

One case this cannot make safe: a daemon too old to know the query stops
anyway, because an unknown query string is not an error. The question is newer
than the daemons that most need to be asked it, and there is no version of this
that reaches back before it existed.

## Windows

It compiles and vets — `make windows` — and nothing here has ever run it. Read
the rest of this section as "what should happen", not "what was seen".

The reason written down for skipping it was wrong: *"`net.Listen("unix")` is
the daemon's foundation and Windows wants a named pipe."* Windows 10 1803 and
later have AF_UNIX, and Go speaks it, so the socket, the client, the version
handshake and the whole HTTP-over-a-socket transport crossed without a line
changing. What actually failed a `GOOS=windows` build was four symbols, none of
them the transport: `syscall.Kill` and `Setpgid` in the bash tool, `Flock` and
`Setsid` here.

They are split into `proc_unix.go`/`proc_windows.go` and
`spawn_unix.go`/`spawn_windows.go` — a file each, because the two systems
disagree about what a process group is and not about what the code is for.

Three things are honestly worse there, each marked where it lives:

- **`killGroup` ends the shell, not the tree.** Windows has no `kill(-pid)`.
  Doing it properly wants a Job Object. A `go test` started by a cancelled
  command keeps compiling.
- **No spawn lock.** `tryLock` returns false, so two front ends starting a
  daemon together both spawn — which they already survived before the lock
  existed, since the loser cannot bind and finds the winner on its next poll.
  Guessing at `LockFileEx` on a platform nothing here can run is how you ship a
  deadlock instead of a lock.
- **`run_bash` needs a bash.** The tool runs `bash -c`, and every command a
  model writes for it is a unix one. Git for Windows or WSL supplies that;
  `cmd.exe` would compile and then fail on the first pipe.

`make check` runs the Windows vet, so the split cannot rot silently. That is
the only guarantee on offer: it builds. Whether the daemon runs on Windows is
unknown, and will stay unknown until somebody runs it.