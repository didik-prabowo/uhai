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

- `provider/openai` — Chat Completions, which OpenAI, OpenRouter, Z.ai and
  Ollama all speak. Only the base URL differs, which is why four vendors
  share one folder: a `provider/zai` would have been the same file twice.
- `provider/anthropic` — the Messages API. Blocks rather than a flattened
  string, `max_tokens` required, and a tool call whose arguments arrive as
  fragments of JSON that are only valid once the block closes.
- `provider/gemini` — generateContent. The assistant is called "model", a call
  and its result are parts of a message, and a result is matched to its call by
  the tool's *name* — so the id every other API hands out has to be looked back
  up while translating, which is the one place the neutral types cost something
  to carry. Gemini 3 adds a second: every tool call carries a
  `thoughtSignature` that has to come back on the same part next turn, or the
  API answers 400. Nothing else in uhai reads the token, so `ContentBlock`
  carries it as an opaque `Signature` and the session stores it — a dropped
  signature only shows up one turn later, on the request after the tool ran.

`config.loadProvider` picks by `API` in the provider table, which the third
format bought: a name compared against a literal was fine for two.

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
