# Documentation

Two audiences, kept apart, because the question "what can this thing do to my
machine?" and the question "why is it built like this?" are rarely asked by
the same person on the same day.

## If you are using uhai

[`guide/tools.md`](guide/tools.md) — every tool the model can call, with its
parameters, its limits, what it refuses and why. It opens with the table of
which ones act without asking, which is the security claim this project makes
about itself.

[`guide/permissions.md`](guide/permissions.md) — how to change that: the three
lists, the rule syntax, what wins when two rules disagree, and what each answer
at the prompt means.

Both pages are held to the code by `internal/tools/rules_test.go`. A tool
without a section, a defaults table that disagrees with `NeedsConfirm`, or a
count written out in prose and left behind all fail the build — so these two
cannot quietly drift from what the program does.

The [README](../README.md) is the shorter way in: what uhai is, how to point
it at a model, and what the terminal does.

## If you are working on uhai

[`roadmap.md`](roadmap.md) — what is built, what is next, and for everything
deliberately unbuilt, the trigger that should start it. It is a record of
decisions rather than a plan with dates: the reason something is missing is
usually more useful than the intention to add it.

[`../AGENTS.md`](../AGENTS.md) is the guide to the source, and the same file
uhai reads into its own system prompt every session. The subsystems keep their
notes under `.uhai/skills/`, opened when they are the thing being worked on
rather than on every turn.

[`../CONTRIBUTING.md`](../CONTRIBUTING.md) is the short version of how to send
a change, including the one convention nobody guesses: a commit message here
carries the reasoning, and what was rejected.
