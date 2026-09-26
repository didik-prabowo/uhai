# Contributing

Patches are welcome. This page is the short version; `AGENTS.md` is the long
one, and it is the guide to the source rather than to the process.

## Before you write anything

    make check

That is gofmt, `go vet`, the Windows build nobody here can run, and the tests.
CI runs the same target, plus `make race`. If it passes before your change and
fails after, the failure is yours.

`CGO_ENABLED=0` is in front of every recipe on purpose: without it the linker
in a sandboxed environment fails to build the test binaries and says nothing
useful about why. `make race` is the one exception — the race detector is
built out of cgo and refuses to run without it.

Go 1.25 or newer, which the Charm v2 packages require. `go.mod` asks for it and
`GOTOOLCHAIN=auto` — the default — fetches it, so nothing has to be installed
by hand.

## The one convention you would not guess

**A commit message carries the reasoning, including what was rejected.**

`AGENTS.md` is the summary of how this project is built; `git log` is the
record of why. A message that says *what* changed is redundant with the diff.
What the diff cannot say is which two approaches you weighed, why the smaller
one lost, and what would have to become true for the other to win. Read three
entries of `git log` before writing your first one — the shape is obvious once
you have.

A commit that takes a deliberate shortcut says so in the code too, with a
`ponytail:` comment naming the ceiling and the way up:

```go
// ponytail: no brace expansion. "{cmd,internal}/**" is still one literal
// string, and nothing promises otherwise; reach for a glob library if full
// glob syntax is ever actually wanted.
```

That is how a known limit stops being a surprise to the next person.

## What the code should look like

- **Comments say why, not what.** Match the density already there — it is
  higher than most Go, deliberately, because this project keeps its decisions
  in the source rather than in a wiki that drifts.
- **Documents are held to the code by tests.** `internal/tools/rules_test.go`
  fails the build if a tool has no section in `docs/guide/tools.md`, if the
  defaults table disagrees with `NeedsConfirm`, or if the tool count in the
  README and the docs has gone stale. Adding a tool means documenting it in
  the same commit; you will not be reminded politely.
- **A test that waits for a goroutine polls at 5ms for five seconds**
  (`waitTries`, `waitFor`). The answer arrives in milliseconds; the budget is
  for a machine busy with something else.
- Prefer deleting to adding. The smallest change that fixes the cause beats
  the smallest change that quiets the symptom.

## Before opening a pull request

Say what you tried and rejected in the description, the same as in the commit.
If the change is visible in the terminal, say what it looks like now.

If it is large, or it changes a decision `AGENTS.md` records, open an issue
first. Not as a formality: those decisions each have a reason written down,
and it is cheaper to argue with the reason than with the diff.

## What is deliberately not built

`docs/roadmap.md` ends with a list of things that were considered and left
out, each with the trigger that would justify building it. If your idea is on
that list, the useful contribution is evidence that its trigger has fired.
