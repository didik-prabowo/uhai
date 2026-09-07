---
name: model-registry
description: The prefix table of context windows, output ceilings and prices: what it is for, and where each figure came from.
---

# The model registry

`config/models.go` answers four questions about a model, and each is answered
because something asks it — a fifth, whether the model reads images, was
removed once it turned out nothing did: the picker was printing "images" beside
models uhai has no way to send an image to, which is worse than saying nothing: how much history fits before `agent` compacts, how
long an answer may be (the Messages API refuses to guess), whether tools may be
sent at all, and what the turn costs — shown in the model picker and in the
status row while the model works.

Prices are list prices per million tokens, and only for models sold by the
vendor that made them. The same open model costs different money at
OpenRouter, or on a machine under the desk, so those carry no price and are
simply not priced: no figure beats a confident wrong one.

It is matched by prefix against the last segment of the name, so a family is
one entry — `claude-`, not one line per release — and
`openrouter/meta-llama/llama-3.3-70b-instruct` finds `llama-3.3` all the same.
Anything unlisted falls back to figures small enough to be safe anywhere: being
wrong low costs an early compaction, being wrong high costs one wasted
request — the agent now answers a rejected-for-length reply by summarising
and asking again, once. Add a
family when a model behaves oddly, not because the table looks short.

## Where the figures come from now

The table is the floor, not the whole answer. `catalog.go` fetches
**models.dev/api.json** — one document listing what every vendor sells, with
`limit.context`, `limit.output` and `cost.input/output/cache_read`, which is
exactly the four things `modelInfo` carries — reduces it to the six providers
uhai speaks to, and overlays the table wherever it has the model itself.

That is what finally sized the two the table said were missing rather than
wrong: Z.ai's glm-5 line and gpt-5, neither of them probeable, because every
paid key on this machine is out of credit and a limit cannot be asked of an
account that cannot spend. glm-5.2 came back 1M context at $1.40/$4.40, gpt-5
400k at $1.25/$10, against the 128k and 32k unpriced family fallbacks they had
been getting. It also corrected a figure nobody had noticed was stale:
claude-sonnet-5 is $2/$10, and the table had been charging it the Sonnet
family's $3/$15.

Three things stay the table's, and each is a bug if it is given away:

- **`Retired`.** models.dev lists Gemini 2.5 with perfectly good figures, and
  Gemini answers 404 for it on a new key. A catalog records what is sold, not
  what has quietly stopped answering.
- **A `MaxOutput` the catalog left at zero.** Five OpenAI entries report a
  context window and no output ceiling. Zero reaches the wire on Anthropic and
  Gemini, where the API refuses to guess.
- **Everything about a local model.** Ollama is absent from models.dev by
  nature: a model under the desk is whatever was pulled, at whatever context it
  was built with.

The overlay is only ever as present as the last successful fetch, which is why
the table is still a table and not a stub. A machine that has never had network
runs on it alone, and nothing says so, because nothing is wrong.

One decision changed as a consequence. Prices used to be "only for models sold
by the vendor that made them" — the same open model costs different money at
OpenRouter, and a hand table cannot track that. models.dev can, and does, per
host. So `openrouter/meta-llama/llama-3.3-70b-instruct` now shows $0.10/$0.32
instead of nothing. The rule was never about the vendor; it was "no figure
beats a confident wrong one", and this is a right one.

Which exposed where the old rule had been holding by luck. It only ever caught
models the table happened not to price, so a provider uhai has no endpoint for
walked straight through it: `cc/claude-opus-5`, a gateway, matched the prefix
`claude-opus` and was billed Anthropic's $5/$25 — for turns drawn from a
subscription's five-hour window, where the marginal cost is zero dollars. Wrong
figure, wrong category.

So `lookup` now drops the prices when the provider is not in `providers` and
not in the catalog. The sizes stay: something has to decide when to compact,
and the family figure is the best guess there is. Being wrong about a window
costs an early summary; being wrong about a price is a number somebody trusts.
A `ponytail:` marks the half left undone — a *known* provider aimed elsewhere
by a `baseUrl` override still prices at the vendor's rate, and fixing that
means reading settings inside a hot, pure lookup.

## Two caches, two clocks

`cache.go` is `~/.uhai/cache/<name>.json`, a value with the time it was
fetched stamped inside it rather than read off the mtime — a `~/.uhai` copied
to another machine should carry the age of the data, not the age of the copy.

- **models.dev, a day.** List prices move on an announcement, not on the hour,
  and the document is four megabytes. Fetched from `loadModelItems`, off the
  UI's thread, behind the "loading models..." the picker already shows. A
  session that never opens the picker never pays for it.
- **A provider's own `/models`, six hours.** zot's figure, and the right order:
  a new model is announced rather than discovered, and being half a day behind
  costs at most one typo note. This is the one that was free before and is not:
  the picker asks every connected provider in turn, one request each, so
  opening `/model` cost four round trips *every time* and showed nothing at all
  with the network down.

Both read stale rather than nothing when the fetch fails, and only a non-empty
answer is ever written. A provider having a bad minute must not be able to
erase the list — an old list is a working picker, an empty one is not. This is
crush's rule, and it logs rather than reports for the same reason: being
offline is routine and the fallback is sound.

`CacheReadUSD` is its own figure rather than a ratio, because the discount is
not one ratio: Anthropic charges a tenth, OpenAI a quarter for gpt-4.1 and a
half for gpt-4o. Zero falls back to `cacheReadRate`, which is Anthropic's
tenth and a guess anywhere else. It started to matter the day the OpenAI-style
client began reading `prompt_tokens_details.cached_tokens`: every vendor there
caches unasked — two identical requests to Z.ai reported 42 cached tokens and
then 5,295 of 5,297 — and all of it used to be priced as fresh input.

The picker lists one provider at a time, whole, ranked by the table — the ones
it has figures for first, then the rest in the order the provider sent.

It got there in three steps, and the middle one was wrong. It began as a *cut*
to six per provider, which was fine until `/` existed: bubbletea's filter
searches the items the list was given, so anything cut was unfindable. So the
cut became a split — six at the top, remainders after every provider's six —
to keep the top of the picker looking as it had. That lasted until a gateway
turned up with twenty models: six showed at the top and fourteen sat below
three providers' worth of rows, which reads as "they are missing", not "they
are further down". One provider is now one block, and the split is gone along
with the constant that sized it.

Typing a name still reaches what no listing mentions, which is how a model the
endpoint forgot to advertise gets chosen.

The search cost three keys rather than any code. Esc had to clear the filter
instead of closing the picker, space had to be a character instead of the skill
picker's toggle, and enter had to keep meaning "this row" rather than
bubbletea's "apply the filter and wait" — the row is already under the cursor.
What the vendor cannot chat with is dropped first — pictures, speech,
embeddings, a batch queue — along with families the table marks `Retired`,
because a listing cannot be asked: Gemini answers 404 for 2.5 on a new key and
goes on returning it from /models. Of what is left, what uhai has figures for
comes first, the rest in the order the provider sent. That order is worth
nothing on its own — Z.ai returns its ten oldest first, so taking the head
showed four releases of GLM 4 and neither 5.3 nor 5.3-flash, the two newest
and the only ones with a price. A dated id and its alias are one model listed
twice, so the dated one goes when the alias is in the same reply — but only
then, since Anthropic lists nothing else.

The window follows the model, not the session, so `/model` and `/connect` move
it (`useProvider` in tea.go).

`/model` checks a typed name against the provider's own list — afterwards, in
the background, and only as a note. Not before, because a list is not the
truth: Z.ai answers `/models` with ten paid models and none of the free ones,
which work perfectly well, so refusing what a vendor forgot to list would block
a working model in order to catch a typo. The typo still gets a line, which is
better than what it used to get: a failed turn one prompt later, with an error
about the model that never mentions the spelling.

Two things keep the note from crying wolf, and both were bugs first:

- **A prefix counts as listed.** An alias is never in the list itself, only
  the dated id it points at, so the exact match warned about
  `anthropic/claude-sonnet-5` — the name uhai recommends and ships as its own
  default. A note that fires on the happy path is a note nobody reads.
- **`Models()` drops only what cannot hold a conversation.** It is two things
  at once — the picker's offer and what this check compares against — so
  anything filtered out is reported as a model the provider never heard of.
  Lacking tool support is not such a reason (uhai runs those with `UseTools`
  off), and neither is a small window or Google's `deprecated`, which is set
  months before a model stops answering.

A name chosen *from* the picker is not checked at all: it came out of that same
list a moment earlier, so the round-trip could only confirm a match it cannot
fail.

## The search that filled a box and filtered nothing

`/` opened the filter, the typed text appeared in it, and every row stayed
visible. Nothing about that looks like a routing bug, which is why it took a
probe to see: bubbles applies a filter **asynchronously**. Typing returns a
`tea.Batch` whose commands work out the matches and send a `FilterMatchesMsg`
back, and that message is not a `tea.KeyMsg` — so it fell past the block that
routes keys to whatever is open and was handed to the chat prompt behind the
picker. The picker was never told what matched.

`Update` now hands an open picker everything, not only key presses, through
`pickerOpen()` — one predicate asked in three places: what to draw, where a key
goes, and where everything else goes. The third is the one that was missing.

This is the same shape as the pasted-key bug a day earlier, and the second time
the fall-through at the bottom of `Update` quietly ate a message meant for
something on top of the chat. The rule worth keeping: **anything drawn over the
chat owns the whole message stream while it is up**, not just the keyboard.
