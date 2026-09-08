---
name: providers
description: The provider contract, the three wire formats behind it, and what each vendor does differently.
---

# Providers

`internal/provider` is the contract: neutral content blocks, a stop reason,
token usage, and a hook for streamed text. `agent` speaks only to it and never
imports a vendor.

Three implementations sit behind it, and the split is by *wire format*, not by
company:

- `provider/openai` — Chat Completions, which OpenAI, Gemini's compat
  endpoint and every gateway speak. Only the base URL differs, which is why
  one folder covers them all: a `provider/zai` would have been the same file
  twice, and when Z.ai, OpenRouter and Ollama were dropped from the shipped
  table it cost nothing — they are reachable as custom endpoints, unchanged.
- `provider/anthropic` — the Messages API. Blocks rather than a flattened
  string, `max_tokens` required, and a tool call whose arguments arrive as
  fragments of JSON that are only valid once the block closes.
There were three. `provider/gemini` spoke generateContent — the assistant
called "model", tool results matched to their call by the tool's *name* rather
than its id, and the model in the URL instead of the body — and it was deleted
once Google's own OpenAI-compatible endpoint turned out to carry the one thing
that seemed to require it.

That thing is Gemini 3's thought signature: every tool call comes back with
one, and it has to go out again on the same call next turn or the API answers
400. It looked like a field with no home in Chat Completions. It has one —
`extra_content` on the tool call — so `wireCall` carries it as an opaque
`json.RawMessage` and hands it back untouched. Nothing reads inside it, which
is what makes one field enough for whichever vendor does this next.

`ContentBlock.Signature` was already the neutral home for it and did not
change; only the client under it did. A dropped signature shows up one turn
late, on the request *after* the tool ran, so it has a test on both directions
plus one that checks an ordinary call grows no empty `extra_content` — some
endpoints validate what they are sent.

What the deletion cost: 701 lines gone, ~15 added, and Google's compat endpoint
namespaces its ids (`models/gemini-3.5-flash`), trimmed in `Models()` because
the model table matches on the last segment and would otherwise miss.

`config.loadProvider` now picks between two, and the openai client is what is
left when neither `case` matches — which is also what makes a gateway need no
code at all.

What the second and third implementations taught:

- `provider.Request` has no `MaxTokens`. Anthropic needs one, so the caller
  passes it in the client's options, out of the registry below, and the neutral
  request stays as it was.
- `Request.Stream` carries text only. Tool arguments stream too, and are
  buffered rather than reported — fine while nothing shows them being typed.
- `Request.Reasoning` carries a thinking model's working out, which the
  vendors send apart from the answer under two different names
  (`reasoning_content`, `reasoning`) and which is emphatically not the answer:
  it is shown dimmed, never kept, never sent back. Mixed into `Stream` it
  would be spoken as the reply and stored in the history as one. GLM made this
  visible — twenty seconds of silence that looked exactly like a hang.
- Tool calls are identified by an id the neutral types require and Gemini does
  not have. That client invents one, which works because the id only has to be
  unique within the conversation it is used in.

The Anthropic client marks the end of the system prompt as a cache
breakpoint. The API renders tools, then system, then messages, so one mark
covers every byte of a request that does not change within a turn — for this
project about nine thousand tokens, resent on each iteration of the tool loop,
so ten tool calls used to mean paying ten times for identical text. That is why
`System` goes as a block rather than a string: a string cannot carry the mark.

One breakpoint, not the four the API allows. The next one worth having is on
the conversation so far, and it has to move every turn; this one never moves,
which is what makes it free to keep correct.

`/cost` is the other half of that. The status row prices a *turn*, which is the
wrong number for "what have I spent": a turn with ten tool calls is charged for
its input ten times and only the last one is ever on screen. `spent` adds every
call up, `/cost` reads it, and it is printed on the way out beside the resume
id — the moment the question actually gets asked. Tokens served from cache get
their own figure there, since that is the difference between this session and
the same session without a breakpoint.

It is in memory and starts again with the process. Persisting it would mean
storing tokens and pricing them later at whatever model happens to be loaded
then, which is a confident wrong figure the first time `/model` is used.

`Usage` counts cached input apart from fresh input, because it is not billed
the same — a read is a tenth, a write a quarter extra, and `config.CostUSD`
prices all three. Folding them together would have made the status row quote a
figure wrong by roughly the whole system prompt, in whichever direction caching
happened to work.

Compaction fires at 85% of the window, not at all of it. Input and output
share the window, so a history that exactly fills it leaves nowhere for the
answer to go and the provider refuses the request rather than trimming it — the
old rule summarised only once the history had already passed the whole window.
The fifteen per cent held back is larger than the longest answer any model here
may write (150k against a 128k ceiling on a million-token window; 4.8k against
4,096 on the 32k fallback), so the headroom is arithmetic rather than a guess.
It also absorbs `Tokens` being loose: characters over four is close for prose
and undercounts code and JSON.

## Thinking

Anthropic's current models think before they answer, and uhai asks them to:
`thinking: {type: "adaptive"}` plus `output_config: {effort: "xhigh"}`, both
gated on `Thinking` in the model table.

Three things about that shape are worth keeping straight, because the obvious
version of each is wrong:

- **`budget_tokens` is not an older spelling of it.** It is *removed* on the 5
  generation — sending it is a 400, not a deprecation warning. There is no
  fallback to write; a model that predates adaptive thinking is sent neither
  parameter, which is what the table's `Thinking` flag decides.
- **Thinking is on whether uhai asks or not**, and asking is about *visibility*.
  `display` defaults to `"omitted"`, so the blocks arrive with empty text: the
  model thinks, the turn is billed for it, and the screen shows a pause where
  the working out should be. `display: "summarized"` is what makes
  `OnReasoning` have anything to draw.
- **Thinking blocks are kept and sent back.** This is the half that is not
  cosmetic. Each block is signed, the API checks the signature against the
  content, and dropping the blocks from the history — or tidying them on the
  way out — is refused on the next turn with an error naming a rule rather
  than the turn that broke it. So `BlockThinking` is a content block like any
  other: stored in the session, replayed unchanged, empty text and all. Empty
  is not absent, and the rule is about what was edited rather than what was
  read.

`effort` is the one quality knob with a bill attached, so it is also a settings
key: `"effort": "low"` in `settings.json` overrides the table. The default is
`xhigh` because that is what the vendor recommends for coding and agentic work,
and the status row prices every turn, so the cost of the choice is visible
where the choice is made.

The gateways get none of this. They speak the OpenAI format, where the field is
`reasoning_effort` and every gateway maps it differently — and uhai already
reads `reasoning_content` back from the ones that send it. *Build the request
half when a gateway is the thing being used for planning.*

## Providers uhai does not ship

`/connect` ends with `+ custom endpoint`, which opens one screen with three
boxes — name, endpoint, key — and writes the base URL to
`~/.uhai/settings.json` and the key to `auth.json`. That was two hand edits
before, and the second one has a mode to get right.

One screen rather than three questions, because all three are copied from the
same page. One `textinput` rather than three, though: only the focused box is
ever typed into, so the other two are strings until they are. Tab parks what is
typed and opens the next, wrapping; enter submits the lot, because a form where
enter meant "next" would need a second key for "done" and there is nowhere
obvious to put it.

Validation happens on submit, all four checks at once, and a bad field puts the
cursor back on itself rather than throwing the other two away — retyping two
fields is a strange punishment for one typo.

Afterwards it opens the model picker **narrowed to that provider**: what the
endpoint serves is the next question and it is one the endpoint can answer. If
it cannot list, the id gets typed instead, which is `fieldCustomModel` and the
only question the form does not ask.

It travels through `askCredential`/`saveCredential` rather than a form of its
own. The name and the URL are not credentials and never reach `auth.json`, but
the machinery for asking one thing at a time already existed for Anthropic's
workspace id, and a second copy of it would have been the same code under a
different name.

Two names had to be told apart, and the distinction is load-bearing:

- **`Known`** — uhai ships an endpoint for it. Unchanged, and still what
  `models.go` asks before it quotes a price. A gateway must never become Known
  or it starts being billed at the vendor's list rate.
- **`Configured`** — Known, or something in `baseUrls` says where it lives.
  This is what `/connect` and `/disconnect` check.

`Providers()` returns the custom ones sorted, then the built-ins sorted. It was
the other way round first, on the argument that a gateway added last week
should not push the vendors about — which was wrong for the case that happens.
The model picker shows six rows per provider, so three built-ins put a
gateway's models on row nineteen, below the fold, and the one provider somebody
had gone to the trouble of registering was the one they had to scroll for.
Registering an endpoint *is* the statement that it is the one being used.

It is in this list rather than one of its own because both pickers and
`/disconnect` read it: a provider that can be connected and not found again is
half a feature.

Three refusals in the form, each of them a bug avoided rather than a rule:

- A name with a slash or a space. `provider/model` would stop parsing.
- A name already in the table. Pointing `anthropic` somewhere else is a
  `baseUrls` override, and calling it custom here would silently reprice it.
- A URL with no scheme. It fails much later otherwise, as a dial error that
  names the host and not the mistake.

`/disconnect` removes a custom provider whole — the base URL as well as the
key. Forgetting only the key leaves it in every picker as a provider that
cannot answer.

### Paste is not a key press

`tea.PasteMsg` is its own message in bubbletea v2, so it never entered the
`tea.KeyMsg` block that routes to whatever is open — it fell through to the
chat prompt behind the picker. An API key pasted into `/connect` went into the
box nobody was looking at and stayed there, one enter away from being sent to
a model. `Update` now routes it the way it routes a key press: the entry box
in key entry, the list while a picker is open, the prompt only when the prompt
is what is in front.

### The picker used to skip them

`loadModelItems` began each provider with `config.DefaultModel(name)` and moved
on when it was empty — which it always is for a provider with no table entry.
So a gateway could be added, appear in `/connect`, and never show a single
model in `/model`. It now builds the client with `config.ListerModel`: the
default when there is one, the selected model when it belongs to this provider,
and a placeholder otherwise. Listing does not send the model anywhere, so the
placeholder costs nothing; without it the picker was blind to exactly the
providers that most needed listing.

## The shipped table is two, not six

`anthropic` and `openai`. Z.ai, OpenRouter, Ollama and then Gemini were in it
and are not any more — deliberately, and at no cost to anyone using them:
`/connect` → `+ custom endpoint` reaches all four, since every one of them
speaks the format the openai client already talks. What the table buys a
provider is a default endpoint, a default model, a key page and a price
bracket; what it costs is a line that has to stay true.

Gemini going took two things with it. `catalogProvider` was a translation table
for the one provider whose models.dev id differed from ours — google — and it
is gone; every name uhai ships now matches, and a future one that does not will
want it back. And `defaultModel` had to move: it was Groq, then
`gemini/gemini-3.5-flash` for the free tier, and with no free provider left it
is `anthropic/claude-sonnet-5` — the one worth paying for rather than the one
that costs least. A first turn that answers well beats a first turn that is
free and wrong.

Two things followed and are worth knowing:

- **The GLM prices went with them.** They were reachable only while Z.ai was a
  provider uhai shipped an endpoint for; a gateway serving GLM is not Known, so
  `lookup` strips the price. The figures were unreachable rather than merely
  stale, which is worse. The windows stayed — those are what a custom endpoint
  compacts against.
- **Gemini is reachable, and unpriced.** The compat endpoint still works as a
  custom endpoint; what it loses is the models.dev figures, since those are
  only looked up for a provider in the table. The `gemini-*` rows in
  `models.go` still size it, and still mark 2.5 and 2.0 `Retired`.
- **Ollama now needs a key it does not have.** `providerInfo.Local` was its
  only user, and the custom-endpoint form requires a key. A local server that
  wants none cannot be added through the form yet. *Fix it when somebody
  actually runs one* — the field is still there, and the form is one condition
  away from honouring it.

### /model refresh

The six-hour list cache is a courtesy until the moment a gateway gains a
provider, and then it is a wall with no door: the endpoint has twenty models,
uhai has the five it saw half an hour ago, and nothing on screen explains why.
`/model refresh` deletes the per-provider lists and reopens the picker.

It leaves `models-dev.json` alone. That one is a public catalog nobody edits
locally, and re-fetching four megabytes is the wrong answer to "I added a model
to my proxy". The two caches answer different questions and only one of them
can be made stale by hand.

`refresh` cannot collide with a model name — those are written
`provider/model` — so the argument needs no flag syntax. This is the
force-refresh that was deliberately skipped when the cache went in, with the
trigger written down as *"add it when somebody is waiting on a new model in
hours rather than days"*. It took one afternoon.

### Newest first, and the sort that undid itself

`Models()` sorts the ids by the endpoint's `created`, newest first, with the id
as the tie-break. A `sort.Strings(ids)` used to follow that loop and replace
the whole ordering with an alphabetical one — the work was done and discarded a
line later.

It hid for a simple reason: a gateway reports no `created` at all, so every
model ties and the tie-break gives id order, which is exactly what the
alphabetical sort produced. The bug was only visible on the endpoints that do
report dates, where the picker offered `gpt-4.1` before `gpt-4o` and buried the
newest release at the bottom of its own block. Anthropic now leads with the
current generation instead of whatever sorts first.
