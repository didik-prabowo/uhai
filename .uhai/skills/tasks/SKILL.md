---
name: tasks
description: Background tasks and sub-agents: how one is started, what it inherits, and how its report comes back.
---

# Tasks

`internal/task` is the bookkeeping — id, status, elapsed, report — and the
queue. Two tasks run at once (`MaxRunning`); the rest wait, showing as `queued`
in `/tasks`. Work somebody is waiting on skips the queue entirely
(`RunNow`, used by the model's `spawn_task`): queueing it behind two long
background jobs stalls the conversation, and looks exactly like a model
thinking very hard. The limit is not about the machine: ten background prompts on one
key are ten simultaneous requests, and the provider answers 429 to all of them.
Work is not faster for being started at the same time.

Elapsed is time spent working, not time spent waiting, so a task that sat in
the queue does not report the wait as its own.

Three doors go through `Registry.Run`, which is why none of them had to learn
about the queue: `/bg` (a prompt run by a nested agent), the model's own
`spawn_task`, and `/check`.

`agent.Asker` names the interface an agent would have — `Ask` and nothing else
— with a compile-time assertion that the agent satisfies it. Nothing consumes
it, which the comment says plainly. It is a marker for a seam that already
exists as a function, not a second one.

`/check` is the other kind of work entirely — a command, no model anywhere in
it, which is why it runs with no provider connected. It is also the answer to
"should there be an Agent interface": two kinds of task now exist and neither
needed one, because `task.Runner` is already the seam. A function was enough.

When a task ends, its result reaches both the user and the model: the line in
the chat, and the report folded into the next prompt, so a failing check is
something the model can answer for rather than something you retype. It travels
with the prompt rather than as a message of its own — the Messages API wants
the roles to alternate, and two user messages in a row would be refused — and
the chat says when it is being sent.

A task can be watched while it works — `/tasks t1` shows what it has printed so
far, line by line for a command and as the answer is written for a prompt — and
stopped with `/stop t1`, queued or running. Stopping kills the command's whole
process group: killing the shell alone leaves `go test` compiling in the
background, which is not what stopping means.

A task that fails keeps what it wrote. It reports nothing — that is what
failing means — so `/tasks t1` falls back to the tail of what it had written,
and `spawn_task` hands the model the same thing under the error. A review that
ran five minutes and died on a rate limit had already found things; throwing
that away and saying only "failed" wastes the work twice, once in tokens and
once in the asking again.

What `/check` runs is the project's own business: `check` in its
`.uhai/settings.json`, or a guess from the files present (go.mod, package.json,
Makefile) when it says nothing.
