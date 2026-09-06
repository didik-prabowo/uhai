---
name: daemon
description: The agent as a process the terminal can outlive — the socket, the second stream, and what has and has not moved onto it yet.
---

# Daemon

`uhai -daemon` runs a process that listens on `~/.uhai/daemon.sock`. It exists
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
own process. The TUI still runs its own agent: wiring it across is the last
step, and the one this repository cannot test itself.

One turn at a time, under a mutex. A conversation is a single history, and two
turns writing to it at once interleave into something neither caller asked for.

The turn does not hold the front end's context. A terminal that hangs up
mid-answer has left the room, not cancelled the work: half a turn in the
history is worse than a whole one nobody watched. Escape is a different thing
and will need a route of its own.

The permission round trip is what made it possible. `Server.Ask` is what a daemon hands the agent as its `Confirm`
hook: the question leaves as an event, the terminal answers with a POST to
`/v1/questions/{id}`, and `Ask` blocks in between. In one process that is a
function call returning a bool — this is the whole reason the move is a piece
of work rather than a move.

Silence is a no, both ways: a cancelled context and an expired deadline both
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
