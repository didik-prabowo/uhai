# Models

uhai does not have a list of models it supports. It has two **wire formats** —
the OpenAI Chat Completions shape and Anthropic's Messages shape — and it will
talk to anything that speaks either one. Whether that is a vendor, a gateway,
your company's endpoint or a model on your own machine is not something it
needs to know.

What it does keep is a note of what each *model* is like: how much history
fits, how long an answer may be, whether it can be sent tools, and what a turn
costs. That is the registry, and most of this page is about where those
figures come from and when they are refused.

## Providers

Two ship with an endpoint:

| | |
|---|---|
| `anthropic` | `https://api.anthropic.com/v1` |
| `openai` | `https://api.openai.com/v1` |

Everything else is `/connect` → **`+ custom endpoint`**: a name, a base URL, a
key. That is not a lesser path — it is how every other provider is meant to be
added, and it needs no code, because the client that talks to OpenAI does not
care who answers.

The name is yours to pick. One word, and it becomes the prefix:

```
name      acme
endpoint  https://acme.example.com/v1
key       ••••••••
```

which makes the model `acme/sonnet-4.5`.

Gemini, Z.ai, OpenRouter and Ollama shipped in that table once and were taken
out. All four speak the OpenAI format, so an entry bought a default URL and a
price bracket and cost a line of code that had to stay true. Their endpoints,
for pasting:

| | endpoint | key from |
|---|---|---|
| Gemini | `https://generativelanguage.googleapis.com/v1beta/openai` | aistudio.google.com/apikey |
| Z.ai | `https://api.z.ai/api/paas/v4` | z.ai/manage-apikey/apikey-list |
| OpenRouter | `https://openrouter.ai/api/v1` | openrouter.ai/keys |
| Ollama | `http://localhost:11434/v1` | any string; it wants none |

## Choosing one

`/model` opens the list of what the connected endpoints report. `/` filters it
as you type.

```
/model                 the picker
/model refresh         ask the endpoints again
```

A provider's list is remembered for six hours, because it changes on an
announcement rather than on the hour, and asking on every open would put a
network round trip in front of a picker. `/model refresh` is for the day a
vendor ships something and you do not want to wait.

**A model the endpoint forgets to list can still be typed by name.** Lists go
stale, and a gateway may not enumerate everything it routes to; refusing to
use a model because it was absent from a list would be uhai overruling you
about your own account.

`/model` changes the model in the middle of a conversation and keeps
everything said so far. The window, whether it thinks and how hard all follow
the new model. Whether the conversation has tools does not — the history
already commits to that, and switching it off midway would strand the tool
calls already in it.

## Setting a default

In `settings.json`, as `provider/model`:

```json
{ "model": "anthropic/claude-sonnet-5" }
```

Without one, uhai starts at `anthropic/claude-sonnet-5`.

A custom endpoint needs its address too, which is what `/connect` writes for
you:

```json
{
  "model": "acme/sonnet-4.5",
  "baseUrls": { "acme": "https://acme.example.com/v1" }
}
```

`baseUrls` points **one** provider somewhere else. There is also a bare
`baseUrl`, which applies to whichever provider is loaded — aiming that at a
proxy for one vendor silently redirects the next `/model` too, so prefer
`baseUrls` unless you mean exactly that.

### Which file wins

Three are read, lowest priority first, and the later ones win key by key:

1. `~/.uhai/settings.json` — yours, and follows you between projects
2. `.uhai/settings.json` — the project's, beside the code
3. `.uhai/settings.local.json` — yours, for this project, gitignored

Then the environment, which beats all three:

```sh
UHAI_MODEL=openai/gpt-5      # the model
UHAI_BASE_URL=http://...     # the bare baseUrl, same caveat as above
```

**Permissions merge differently from the model.** A model is replaced by the
nearer file; permission rules are *appended*, so a project adds to what your
own settings already say rather than starting the list over. Denials
accumulate, which is the safe direction for a list to grow in. See
[permissions.md](permissions.md).

## Keys

Credentials live in `~/.uhai/auth.json`, mode `0600`, one entry per provider.
`/connect` writes it and `/disconnect` removes an entry.

Read lowest to highest:

1. `auth.json`
2. the vendor's own variable — `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`
3. `UHAI_API_KEY`, which supplies *every* provider's key

That last one is a wildcard, and it is right for somebody pointing uhai at a
single gateway. It is also why the environment winning matters: saving a new
key while one is exported changes nothing, and `/connect` says so rather than
letting you wonder why.

When a key stops working — expired, revoked, out of credit — `/connect <name>`
again and paste a new one. Escape keeps the one already saved.

## What uhai knows about a model

Four things, and each is there because something asks:

| | why it is needed |
|---|---|
| context window | when to compact the history |
| max output | the Messages API refuses to guess |
| tool support | whether tools may be sent at all |
| price per million | the status row, and the model picker |

The figures come from two places. `config/models.go` is a table matched by
**prefix against the last segment of the name**, so a family is one entry —
`claude-`, not a line per release — and
`openrouter/meta-llama/llama-3.3-70b-instruct` still finds `llama-3.3`.

Over that, `config/catalog.go` fetches [models.dev](https://models.dev)'s
public catalog once a day and overlays it, which is how uhai carries real
figures for models nobody here can probe. Three things stay the table's, and
each is a bug if given away:

- **Retired models.** A catalog records what is sold, not what has quietly
  stopped answering — Gemini 2.5 is listed with perfectly good figures and
  returns 404 on a new key.
- **An output ceiling the catalog left at zero.** Zero reaches the wire on
  Anthropic and Gemini, where the API refuses to guess.
- **Anything local.** A model under your desk is whatever was pulled, at
  whatever context it was built with, and no catalog can know that.

A machine that has never had network runs on the table alone, and nothing says
so, because nothing is wrong.

**An unknown model is not refused.** It falls back to figures small enough to
be safe anywhere. Being wrong low costs an early compaction; being wrong high
costs one rejected request, which the agent answers by summarising and asking
again, once.

## Behind a gateway

This is where a model's *name* stops being a model.

Some gateways sell an alias that routes over several models, picked per turn.
`plan-deep` is not a model; it is a decision made after you press enter. Sized
from the name, it matched no family and fell back to a small default — so a
conversation with a million-token model was summarised away every 27k tokens,
against nothing on screen saying why. From uhai's side nothing was wrong: it
was measuring against the name it had been given.

So uhai reads the model that **actually replied** out of the response and
sizes the turn from that. The first turn of a session still goes out at the
alias's figures, because nothing has answered yet.

There is a second rule, and it is about money rather than size:

> **A gateway is sized but never priced.**

Knowing a model's context window is a fact about the model. Quoting a price is
a claim about somebody's billing. A gateway called `cc` serving Opus once
matched the prefix `claude-opus` and was billed Anthropic's list price — for
turns drawn from a subscription, where the marginal cost was nothing. Wrong
figure, wrong category.

Two words carry that rule, and they are worth knowing if you read the code:

- **Known** — uhai ships an endpoint for this provider. It gates whether a
  price may be quoted, and a gateway must never become Known.
- **Configured** — Known, *or* a `baseUrl` says where it lives. It gates the
  pickers.

So a custom endpoint appears everywhere you need it and the status row says
nothing about cost, rather than a confident wrong figure.

## Where to look next

[features.md](features.md) for what the rest of uhai does,
[tools.md](tools.md) for what a model is allowed to call, and
[permissions.md](permissions.md) for how to change that.
