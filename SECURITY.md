# Security

## Reporting something

Open a [security advisory](https://github.com/didik-prabowo/uhai/security/advisories/new)
rather than an issue, and you will get an answer. This is one person's project,
not a company with a rota — expect a reply in days, not hours, and no bounty.

If the advisory form is not available to you, an ordinary issue that says only
"I have found a security problem, how do I reach you" is fine. Do not put the
details in it.

## What uhai is

An agent that reads and writes your files, runs shell commands, and sends
parts of your code to a model provider you choose. Every one of those is a
capability, not a bug, so the security of it is entirely about **what happens
without you saying so**. A report is most useful when it shows one of these
being false:

- A tool that changes something, or leaves the machine, running without a
  confirmation. `docs/guide/tools.md` has the defaults table and
  `internal/tools/rules_test.go` fails the build if it and the code disagree.
- A permission rule being bypassed — a `deny` that does not deny, a shell
  line whose parts are not judged separately, a tool reachable under a name
  no rule can match.
- `fetch_url` or `search_web` reaching something that is not the public
  internet: loopback, a private range, or `169.254.169.254`, which is one
  address away from a cloud machine's credentials. The check runs before the
  request and again on every redirect.
- A credential leaving the place it belongs — written somewhere other than
  `~/.uhai/auth.json` at `0600`, logged, put in a tool result, or sent to a
  provider that is not the one it is for.

## What is not a vulnerability here

- **The model deciding to do something you allowed.** If `run_bash` is on
  `allow`, a command you did not expect is the setting working. That is what
  the confirmation exists for, and `a` only lasts the session.
- **Prompt injection changing what the model does.** A file or a fetched page
  can carry instructions, and uhai does not defend against that — nothing
  does, reliably. It is the reason the tools that act ask first. A report is
  interesting if injection reaches something that *should* have asked and
  did not.
- **An absolute path escaping the project.** Deliberate: what may be touched
  is answered by the allow/ask/deny lists, and confining paths here would
  quietly change what a rule somebody wrote means.
- **Anything in a dependency**, unless uhai's use of it is what makes it
  exploitable. Report those upstream.

## Versions

There is one: `main`. There are no releases to backport to yet, so a fix
lands there and you rebuild.
