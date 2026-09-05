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
wrong low costs an early compaction, being wrong high costs the turn. Add a
family when a model behaves oddly, not because the table looks short.

The picker shows six models per provider, and chooses the six by the table.
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
