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

**The main conversation has not moved.** The front end still owns that agent
and still calls it through Go closures. Permission is why, and it is a piece of
work of its own.

Both registries number from `t1`, so a task the daemon holds is shown as `d1`
and `/stop d1` routes there. Without the prefix, `/stop t1` is ambiguous the
moment a session has one of each — which happens as soon as the model calls
spawn_task while a daemon is running.

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
