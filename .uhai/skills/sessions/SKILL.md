---
name: sessions
description: What a session stores, how ids are made, how one is picked to resume, and where the store's boundaries are.
---

# Sessions

One conversation, one file under `~/.uhai/sessions`, rewritten after every
turn. The name is the id, and the id says nothing: eight random bytes in
base32, thirteen characters, `gijvqxlaulhjq`. `-resume` takes any prefix that
names only one, so four characters is normally enough to type.

It used to be the start time, which read well and sorted for free — and was
wrong twice. It was only good to the second, so two conversations begun in the
same second were one file and the second to save replaced the first, whole and
without a word; a pid was pinned on the end to fix that. And a name that
encodes when a thing was made states a fact the file already carries, which
makes the copy in the name the one that can go stale — a resumed conversation
kept the date it was first opened.

So when, and who holds it, are fields — `started`, `updated`, `pid` — and
`-sessions` prints them. `pid` is rewritten on every save, because a resumed
conversation is held by the process resuming it, not by the one that opened it
in August. Sessions written before it existed show a blank rather than `pid 0`.

`spend` is the other field the file carries, and it is a list rather than one
total: what the conversation cost, split by the model that cost it. A single
stored total would have to be priced later at whatever model was loaded then,
which is the wrong figure the moment `/model` is used mid-conversation — so
the tokens are stored as the fact they are, and each share is priced with its
own model's rates and the shares added. Sessions written before it existed
have no `spend` and report nothing, which is honest: nobody was counting.

Nothing sorts on the file name any more; `All` sorts on `updated`. That cost
nothing: it already read every file whole to build the listing, which is the
`ponytail:` note in the same function.

The id is minted by `session.New` at startup rather than at save time, because
the prompt prints it on the way out and it has to be the string that ends up on
disk. Old date-named files still list and resume: an id missing from the JSON
falls back to the file name.

It lives in `internal/session`, not in `config`, because the two are opposites:
settings, keys and permissions are input a person writes to change what uhai
does, and a session is output uhai produces by running. It sat in `config` for
a while on the strength of sharing a directory — and nothing in `config` ever
referenced a `Session`, which is what gave the mistake away.

`Session` is the data; `Store` is the contract for keeping it — `Save`, `All`,
`Load`. Both live in `internal/session`, which holds no storage at all. The
implementation that ships is `internal/session/filestore`, behind
`filestore.New(dir)`, and a second one is a sibling package: `pgstore`,
`sqlitestore`. The concrete type is unexported — `New` hands back the contract,
since a caller that could name the type would be back where it started.

The split is a package boundary rather than a file boundary because a file
boundary is only a convention: in one package nothing stops `session.go`
importing the disk tomorrow. The import graph is the proof now —
`internal/session` pulls in neither `encoding/json` nor `path/filepath`, and
`go list -deps ./internal/session` does not mention `filestore`. It points one
way and cannot point back.

That forced `Pick`, the prefix rule, to be exported: `filestore` lives outside
the package and needs it, and so will the next backend. Which is the argument
for the shape — the rule is shared rather than reimplemented, and `Load` means
the same thing in every store. `NewID` is exported for the same reason: a store
handed a `Session` with no id has to name it. `orchestrator` names
the store in one line and hands it to `cli.UseStore`; nothing downstream ever
learns which it got. A second backend replaces that line and changes nothing
else.

`Latest` is a package function taking a `Store` rather than a fourth method,
since every backend can answer it from `All`. One that could do better —
`ORDER BY updated DESC LIMIT 1` — should grow an optional interface the way
`provider.ModelLister` does, instead of making every implementation carry a
method most would fake. `pick`, the prefix rule, sits outside the storage for
the same reason: `Load` means the same thing everywhere, and a backend
reinventing what a prefix is would be a bug nobody could see. Export it when
a backend lives outside this package.

The argument against building this was that a Go package is already a seam —
nothing outside `internal/session` knew a session was a file, so swapping the
insides for sqlite would have changed no caller either way. What the interface
buys over that is two implementations at once, and the honest version of it
has two: `TestStoreContract` runs the same scenario against `Files` and
against an in-memory store, so the contract is checked rather than asserted.
A backend added later is finished when that test passes for it. That test is
`package session_test` — an external test package, which is what lets it import
`filestore` without `session` importing it back.

`cli` keeps the live one in a package-level `current`, renamed from `session`
when the package took that name.

All three ways of asking save: the terminal after every turn, the pipe after
every line, and `-p` after its one answer. `-p` did not, for a while, and not
by decision — it answers from `orchestrator.RunOnce`, which bypasses `cli`
entirely and so never met `saveSession`. An answer that cannot be resumed and
does not appear in `-sessions` is one nobody can prove happened. The function
is `cli.SaveSession` now, for the same reason `cli.ContinueSession` is
exported.

A failed turn is saved too, everywhere. The tokens were paid for, and the
history `closeTurn` leaves behind is exactly what a `-resume` carries on from.

Two things `-resume` does that took a bug to learn:

- **It brings back the model.** `Session.Model` was written and never read, so
  a conversation held on Sonnet quietly carried on with whatever
  `settings.json` said today — a different window and a different price, with
  nothing on screen saying so. When that model can no longer be built, the
  fallback is a line on stderr rather than a silent swap.
- **It keeps writing to the same file.** Resuming used to copy the history into
  a new session, so an id nobody could hold on to grew a new twin every time.
  `cli.ContinueSession` hands the id and the start time back.

An id is matched against what was found rather than pasted into a path, so it
cannot be used to read a file elsewhere. Sessions saved before ids existed
still resume: the id comes from the file name, and `updated` falls back to
`started`.

The neutral types carry `json` tags spelling their own field names —
`provider.Message` and `provider.ContentBlock` are what a session file is made
of, so those two are the file format whether or not anyone meant them to be.
Untagged, renaming `ToolUseID` would have changed what is written without
touching a line of storage code, and every saved conversation would have come
back with its tool calls empty and nothing saying why. The tags are capitalised
because that is what was already on disk; snake_case is a migration rather than
a tidy-up, since Go's case-insensitive key matching rescues `Role` but never
`ToolUseID`. `TestSavedSessionKeepsItsWireNames` fails if the format moves.

### Credentials

`auth.json` maps a provider to a *map* of fields rather than to a key, because
one key is not always the whole story: an Anthropic key linked to an identity
can act in several workspaces, and the API answers 400 until the request names
one. `providerInfo.Extra` lists what `/connect` asks for after the key, one
question at a time, and `Save` merges rather than replaces — answering the
second question must not erase the first.

The field was declared with its env var months before anything read it, which
is the failure worth remembering: the store knew about workspaces, the client
never sent the header, and the error the API returned named a thing the code
already had a constant for.
