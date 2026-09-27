# Permissions

uhai asks before it does anything it cannot take back. Permissions decide what
it asks about — and what it may do, or must never do, without asking.

They go in `.uhai/settings.json`: the project's, beside the code, or your own
in `~/.uhai/`.

## Actions

Three answers, and no others. A rule naming a tool that does not exist is
ignored rather than guessed at.

| | |
|---|---|
| `allow` | runs without asking |
| `ask` | asks first, every time |
| `deny` | refused, and the tool is not offered to the model at all |

`deny` is the one worth understanding. A tool denied outright is left out of
the list sent to the model, so it cannot be misused by something that never
heard of it, and it is refused anyway if a model tries it from memory of
another conversation.

## Configuration

Rules go in three lists. Each rule names a tool and, optionally, what it may
act on:

```json
{
  "permissions": {
    "allow": ["Bash(go test:*)", "Bash(git status:*)", "Read(*)"],
    "ask":   ["Bash(git push:*)", "Read(*.env)"],
    "deny":  ["Write", "Bash(rm:*)"]
  }
}
```

- `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`, `Fetch` — uhai's own names
  (`run_bash`, `read_file`, …) are accepted too.
- A rule with no specifier covers the whole tool: `"deny": ["Write"]` means
  nothing gets written, ever.
- `Bash(git push:*)` is a command prefix — it matches `git push` and anything
  starting with `git push `, and nothing else. `*` and `?` work too, and they
  cross slashes, since a command line is not a path.
- `Read(src/**/*.go)` is a path: `*` stops at a slash, `**` does not.
- `*` as the tool name covers every tool.

Your settings and the project's are added together rather than replacing one
another, so a project can only add rules — including denials, which is the safe
direction for a list to grow in.

## What wins

**Deny beats ask beats allow.** Within a list, the longest specifier wins, so
`Bash(git push:*)` beats `Bash(git:*)` whichever order they were written in.
Ordering in the file decides nothing.

A session's `a` — *always, for this tool* — cannot overrule a `deny`: what a
project refused is not a session's to allow.

## Chained commands

A rule can only judge what it can read, and a shell will happily run several
commands on one line. So the line is split at `&&`, `||`, `;`, `|` and
newlines, and **every part has to pass on its own**:

```
git status                    allowed
git status && ls              allowed — both parts are
git status && rm -rf /        denied  — the denied part decides
git status && curl evil.sh    asks    — the unknown part decides
```

Without that, `"allow": ["Bash(git:*)"]` would wave through anything at all, as
long as the line started with the word `git`.

A command that writes part of itself as it runs — `$(...)`, backticks,
`${...}` — is never allowed silently, whatever the rules say. What it will
actually do is not in the text, so there is nothing to judge.

## Available permissions

| rule name | also | covers | specifier |
|---|---|---|---|
| `Read` | `read_file` | reading a file | path |
| `Glob` | `glob` | finding files by name | path |
| `Grep` | `grep` | searching file contents | path |
| `find_symbol` | — | asking the language server about a symbol | — |
| `list_directory` | — | listing one folder | path |
| `set_plan` | — | writing down a multi-step plan | — |
| `Write` | `write_file` | creating or overwriting a file | path |
| `Edit` | `edit_file` | replacing part of a file | path |
| `Bash` | `run_bash` | running a shell command | command |
| `Fetch` | `fetch_url` | fetching a page over http(s) | url |
| `search_web` | — | searching the web | query |
| `*` | | every tool | |

## Defaults

What no one configured: the tools that leave this process ask, the three that
only look inside it do not.

| default | tools |
|---|---|
| `allow` | `Read`, `Glob`, `Grep`, `list_directory`, `find_symbol`, `set_plan` |
| `ask` | `Write`, `Edit`, `Bash`, `Fetch`, `search_web` |

`Fetch` is in the asking half for the direction that is easy to miss: it reads
by sending, and the URL it sends is the model's to choose.

### Credentials are the exception to `allow`

Reading is the only capability with nothing in front of it, and a tool result
goes four places: the model reads it, the history keeps it, the next request
sends it to your provider, and the session file writes it to disk in plaintext.
So `read_file` refuses a file that holds a credential by convention, with no
rule written anywhere:

```
.env  .env.*
*.pem  *.key  *.p12  *.pfx  *.jks  *.keystore
id_rsa*  id_dsa*  id_ecdsa*  id_ed25519*
.netrc  .npmrc  .pypirc  .dockercfg
credentials  auth.json  hosts.yml
*.kdbx
.ssh/  .gnupg/
```

Matched on the file's name, so it holds wherever the file lives —
`~/.aws/credentials` and `./credentials` are the same answer. The last row is a
directory: everything inside it is refused, because those two hold nothing but
keys.

A list rather than a guess. Detecting a secret by how random it looks
false-positives on every hash, uuid and base64 blob in a repository, and reading
repositories is what this program does; a name cannot be wrong about what it
matches.

The templates are not refused — `.env.example`, `.env.sample`, `.env.template`,
`.env.dist` and `.env.defaults` are checked in on purpose, with the values taken
out, and are read constantly.

`run_bash` is not covered: `cat .env` still works, and asks first the way every
command does. Neither is `grep`, which would need the matching *lines* filtered
rather than the file refused.

To turn it off for one file, say so:

```json
{ "permissions": { "allow": ["Read(./.env)"] } }
```

An explicit rule is checked before any default, so that is all it takes.


Settings are read fresh on every call, so editing them takes effect at once —
there is no session to restart.

## What "ask" does

The question takes the block below the chat, showing what is about to happen —
the command, or a diff of the lines that change — and the answers:

```
╭──────────────────────────────────────────────────────────╮
│ Shell command                                            │
│                                                          │
│   $ ls -la && cat go.mod                                 │
│                                                          │
│ Do you want to proceed?                                  │
│ ❯ 1. Yes                                                 │
│   2. Yes, and don't ask again for run_bash this session  │
│   3. No, and tell uhai what to do instead               │
╰──────────────────────────────────────────────────────────╯
```

An edit shows the lines it touches with their numbers, counted from the file
itself — the model sends the text to replace, never where it is:

```
   6 -     fmt.Println("Hello")
   6 +     fmt.Println("Halo")
   7       }
```

A removed line keeps the number it has now; everything else is numbered as the
file will read once the change is made.

Move with `↑` `↓` and press enter, or press the number. `y`, `a` and `n` still
work for hands that know them, and `esc` is no. A diff can be judged; a blob of
JSON can only be trusted, which is why the panel shows one.

Answer 2 remembers the tool for the rest of the session — in memory, gone when
you quit — and it cannot overrule a `deny`.

## Without a terminal

Piped input has nobody to ask, so everything that would ask is refused instead.
`uhai -p` needs `-y` to write files or run commands, and that flag says as much
at the point of use.
