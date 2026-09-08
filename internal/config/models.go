package config

import (
	"fmt"
	"strings"

	"github.com/didik-prabowo/uhai/internal/provider"
)

// What is known about a model: what it can hold, how much it may write, what
// it can be given, and what it charges. Every field is here because something
// asks for it — the agent compacts against Context, the Messages API refuses a
// request without MaxOutput, the model picker shows the rest, and the status
// row prices the turn.
//
// Context reaches every vendor. MaxOutput reaches only Anthropic and Gemini,
// which are the two clients settings.go hands it to; the OpenAI-style endpoints
// are left to their own defaults, so their MaxOutput here is documentation and
// nothing reads it.
//
// Prices are list prices in US dollars per million tokens, and only for models
// sold by the vendor that made them. The same open model costs different money
// at OpenRouter or on a machine under the desk, so those are left at zero
// and simply not priced: no figure is better than a confident wrong one.
type modelInfo struct {
	Context   int
	MaxOutput int

	InputUSD  float64 // per million tokens, 0 when it depends on the host
	OutputUSD float64

	// CacheReadUSD is what a token served from the vendor's cache costs, per
	// million. Its own figure because the discount is not one ratio: Anthropic
	// charges a tenth, OpenAI charges a quarter for gpt-4.1 and a half for
	// gpt-4o. Zero falls back to cacheReadRate, which is Anthropic's tenth and
	// a guess anywhere else.
	CacheReadUSD float64

	// NoTools marks the rare model that cannot be given tools, so the zero
	// value is the common case: it can.
	NoTools bool

	// Thinking says the model takes adaptive thinking, and Effort how hard to
	// ask it to think. The zero value is off for both, which is right: the
	// generation before this one refuses the parameters outright, and a
	// request refused for asking to think is worse than one that does not ask.
	//
	// ponytail: the family rows describe the current generation, the way
	// Context and MaxOutput already do — so 4.6, which takes adaptive thinking
	// but not "xhigh", would be sent an effort it refuses. Give it a row of
	// its own the way claude-opus-4-5 has one, if anybody runs it.
	Thinking bool
	Effort   string

	// Retired marks a family the vendor still lists but no longer serves.
	// Gemini answers 404 for 2.5 on a new key while /models goes on returning
	// it, so the list cannot be asked — only the table can say.
	Retired bool
}

const (
	defaultContext   = 32_000
	defaultMaxOutput = 4_096
)

// models is matched by prefix against the model's own name, so a family is one
// entry rather than one per release. The longest match wins, which is what
// lets a specific model correct its family.
//
// Checked against the providers themselves on 2026-09-06, and half of them do
// report their own limits after all: Anthropic's /v1/models carries
// max_input_tokens and max_tokens, Gemini's carries inputTokenLimit and
// outputTokenLimit. Those two families are the API's numbers, not a page's.
// OpenAI and Z.ai report ids only, so theirs are read off the pricing pages or
// out of an error message — Z.ai names its output ceiling when you ask for too
// much ("限制数值范围[1,131072]"), which is the whole documentation there is.
//
// Since catalog.go this is the floor rather than the whole answer: models.dev
// overlays it for any model it carries, which is what finally sized the two
// that could not be sized here — Z.ai's glm-5 line and gpt-5, neither of them
// probeable, because every paid key on this machine is out of credit and a
// limit cannot be asked of an account that cannot spend.
//
// What is left is what a catalog cannot know: Retired, and every figure for a
// model running under somebody's desk. And the floor still matters, because
// the overlay is only ever as present as the last successful fetch.
var models = map[string]modelInfo{
	// The 5 generation moved to a million-token window and a 128k answer, and
	// Opus came down to a third of what Opus 4 cost. 128k output is only safe
	// because this client always streams (Stream: true, anthropic.go): the
	// Messages API wants streaming for a max_tokens that large.
	//
	// claude- is the fallback for anything older or unrecognised, and stays at
	// the figures that are safe everywhere.
	// Thinking and Effort are the 5 generation's: adaptive thinking, and
	// "xhigh" — one step below "max" — which is what the vendor recommends for
	// coding and agentic work and what Claude Code itself runs at. Haiku is
	// left out because 4.5 predates both parameters and refuses them.
	"claude-fable":  {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 10, OutputUSD: 50, Thinking: true, Effort: "xhigh"},
	"claude-opus":   {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 5, OutputUSD: 25, Thinking: true, Effort: "xhigh"},
	"claude-sonnet": {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 3, OutputUSD: 15, Thinking: true, Effort: "xhigh"},
	"claude-haiku":  {Context: 200_000, MaxOutput: 64_000, InputUSD: 1, OutputUSD: 5},

	// The 4.5 releases answer shorter than the families they belong to, and
	// MaxOutput is one of the two figures that reaches the wire: asking Opus
	// 4.5 for its family's 128k output is a request the API refuses. Their
	// prices are the family's, which is what they were already being given.
	"claude-opus-4-5":   {Context: 200_000, MaxOutput: 64_000, InputUSD: 5, OutputUSD: 25},
	"claude-sonnet-4-5": {Context: 1_000_000, MaxOutput: 64_000, InputUSD: 3, OutputUSD: 15},

	"claude-": {Context: 200_000, MaxOutput: 8_192},

	// The cached figures are second-hand — read off Gitlawb/zero's catalog,
	// which cites platform.openai.com/docs/pricing — and they are here rather
	// than left to the fallback because they disagree with it and with each
	// other: half for 4o, a quarter for 4.1, against Anthropic's tenth.
	"gpt-4o-mini": {Context: 128_000, MaxOutput: 16_384, InputUSD: 0.15, OutputUSD: 0.60, CacheReadUSD: 0.075},
	"gpt-4o":      {Context: 128_000, MaxOutput: 16_384, InputUSD: 2.50, OutputUSD: 10, CacheReadUSD: 1.25},
	"gpt-4.1":     {Context: 128_000, MaxOutput: 16_384},

	// The only figures here that were not read off a pricing page: Gemini's
	// /models reports inputTokenLimit and outputTokenLimit per model, and the
	// gemini-3 flash family answers 1048576 and 65536. 2.5 is retired — it
	// answers 404 for new keys — and is kept only for old sessions to resume.
	"gemini-3":   {Context: 1_000_000, MaxOutput: 65_536},
	"gemini-2.5": {Context: 1_000_000, MaxOutput: 8_192, Retired: true},
	"gemini-2.0": {Context: 1_000_000, MaxOutput: 8_192, Retired: true},

	// GLM, and the sizes only. They used to carry Z.ai's own prices, which
	// were reachable while Z.ai was a provider uhai shipped an endpoint for.
	// It is not one any more, so whoever serves a GLM now is a gateway — and
	// lookup strips a price for those, which made these figures unreachable
	// rather than merely stale. The windows still earn their place: they are
	// what a custom endpoint pointed at GLM compacts against.
	"glm-4.6": {Context: 200_000, MaxOutput: 8_192},
	"glm-":    {Context: 128_000, MaxOutput: 8_192},

	// Open models: hosted by everyone, priced by each host, so no figures.
	"llama-3.3":     {Context: 128_000, MaxOutput: 8_192},
	"llama-3.1":     {Context: 128_000, MaxOutput: 8_192},
	"qwen2.5-coder": {Context: 32_768, MaxOutput: 4_096},
}

// infoFor looks up "provider/model", or the bare model name. A model name may
// itself carry slashes — "openrouter/meta-llama/llama-3.3-70b-instruct" — so
// the match is made on the last segment, which is where the family lives.
func infoFor(modelSetting string) modelInfo {
	info, _ := lookup(modelSetting)
	return info
}

// lookup is infoFor plus the prefix that matched, which is how Known tells an
// entry written for a model from the family fallback that caught it.
func lookup(modelSetting string) (modelInfo, string) {
	// providerName, not provider: the package of that name is imported here.
	providerName, model, qualified := strings.Cut(modelSetting, "/")

	// The family lives in the last segment either way: a bare name is all
	// there is, and an OpenRouter id carries a second slash of its own.
	family := modelSetting
	if i := strings.LastIndex(family, "/"); i >= 0 {
		family = family[i+1:]
	}
	family = strings.ToLower(family)

	best := modelInfo{Context: defaultContext, MaxOutput: defaultMaxOutput}
	match, longest := "", 0
	for prefix, info := range models {
		if strings.HasPrefix(family, prefix) && len(prefix) > longest {
			best, match, longest = info, prefix, len(prefix)
		}
	}

	// models.dev overlays the table wherever it has the model itself, since
	// its figures are maintained and these were read off pricing pages by
	// hand. Three things stay the table's: Retired, which models.dev does not
	// record; a MaxOutput it left unsaid; and a NoTools already known here,
	// since the table only ever says so about a model that proved it.
	//
	// The match returned is the model's own name, because that is what a hit
	// here is — the exact model, not the family that would have caught it. No
	// vendor's id ends in a dash, so KnownModel still tells the two apart.
	if qualified {
		if info, ok := catalogInfo(providerName, model); ok {
			info.Retired = best.Retired
			info.NoTools = info.NoTools || best.NoTools
			if info.MaxOutput == 0 {
				info.MaxOutput = best.MaxOutput
			}
			return info, family
		}
	}

	// A provider uhai has no endpoint for is a gateway, a proxy, or somebody's
	// own server, and what it charges cannot be known from here: the same
	// model behind it may be billed per token, drawn from a subscription's
	// window, or free. The table's figures are the vendor's own list prices,
	// which is the confident wrong answer this file exists to avoid — a
	// gateway called "cc" serving claude-opus-5 was being billed at Anthropic's
	// $5/$25 for turns that cost nothing.
	//
	// The sizes stay, because those are safe to guess and something has to
	// decide when to compact. Being wrong about a window costs an early
	// summary; being wrong about a price is a number somebody trusts.
	//
	// Only this path needs it: the catalog index holds nothing but providers
	// uhai has an endpoint for, so a hit above is a known provider by
	// construction.
	//
	// ponytail: a known provider pointed somewhere else by a baseUrl override
	// still prices at the vendor's rate — aiming "anthropic" at a proxy keeps
	// Anthropic's figures. Reading settings here would make a hot, pure lookup
	// depend on a file; do it when somebody actually runs that way.
	if qualified && !Known(providerName) {
		best.InputUSD, best.OutputUSD, best.CacheReadUSD = 0, 0, 0
	}
	return best, match
}

// Known reports whether the table has figures for this model in particular
// rather than for its family. A family entry is spelled with the trailing
// dash it matches on — "glm-", "claude-" — so the two are told apart by the
// shape of the key, with nothing extra to keep in step.
//
// The picker asks, because a provider's own list has no order worth trusting:
// Z.ai returns its ten models oldest first, so keeping the first six dropped
// glm-5.3 and glm-5.3-flash, the two newest and the only ones with a price.
func KnownModel(modelSetting string) bool {
	_, prefix := lookup(modelSetting)
	return prefix != "" && !strings.HasSuffix(prefix, "-")
}

// notChat are the parts of a name that mark a model uhai cannot hold a
// conversation with — pictures, speech, embeddings, a batch queue that answers
// hours later. Matched as substrings because no vendor agrees where to put
// them, and kept to what has actually turned up in a real /models reply.
var notChat = []string{
	"-image", "-tts", "-transcribe", "embedding", "-live",
	"computer-use", "deep-research", ":batch",
}

// Recommendable reports whether a model belongs in the picker at all. Being
// listed by the vendor is not enough: Gemini returns forty models, of which
// the first six alphabetically are three retired and three that cannot chat,
// which is how the picker came to offer no live Gemini model at all.
//
// It is only about what to *suggest*. Anything here can still be typed, and an
// old session resumes on a retired model without complaint.
func Recommendable(modelSetting string) bool {
	info, _ := lookup(modelSetting)
	if info.Retired {
		return false
	}
	name := modelSetting
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	for _, mark := range notChat {
		if strings.Contains(name, mark) {
			return false
		}
	}
	return true
}

// ContextWindow is how much history a model will take, in tokens. The agent
// compacts before reaching it.
func ContextWindow(modelSetting string) int { return infoFor(modelSetting).Context }

// MaxOutput is the longest answer to ask for. Anthropic requires it; the
// OpenAI-style endpoints are left to their own defaults, which is what they
// were doing before there was a table to ask.
func MaxOutput(modelSetting string) int { return infoFor(modelSetting).MaxOutput }

// Thinks reports whether the model takes adaptive thinking. Unknown models are
// assumed not to: the parameter is refused by everything older than the 4.6
// generation, and asking is a 400 on every turn rather than a worse answer.
func Thinks(modelSetting string) bool { return infoFor(modelSetting).Thinking }

// Effort is how hard to ask the model to think, "" for the API's own default.
// A setting overrides the table, since the table's answer is a quality choice
// and the bill is the user's: the status row prices each turn, so the cost of
// thinking harder is visible where the choice is made.
func Effort(modelSetting string) string {
	if s, err := LoadSettings(); err == nil && s.Effort != "" {
		return s.Effort
	}
	return infoFor(modelSetting).Effort
}

// SupportsTools reports whether the model can be given tools. Unknown models
// are assumed to manage: nearly all do, and refusing to try would be worse
// than the error the provider returns.
func SupportsTools(modelSetting string) bool { return !infoFor(modelSetting).NoTools }

// CostUSD prices one turn, "" when the model's price depends on who is hosting
// it. Rounded to something a person can read rather than to the cent, since
// a turn often costs less than one.
// Cached input is not billed like fresh input: reading a prefix back costs a
// tenth, and writing one costs a quarter extra the once. Pricing them as plain
// input would overstate a cached turn by roughly the whole system prompt,
// which is the larger half of every request here.
const (
	// Anthropic's ratios, and the fallback for a model with no figure of its
	// own. Wrong for OpenAI, which is why CacheReadUSD exists.
	cacheReadRate  = 0.1
	cacheWriteRate = 1.25
)

func CostUSD(modelSetting string, u provider.Usage) string {
	return FormatUSD(CostOf(modelSetting, u))
}

// CostOf is the same sum as a number, so a conversation held across two models
// can be priced a share at a time and added up. 0 for a model with no price,
// which is the same thing CostUSD says with "".
func CostOf(modelSetting string, u provider.Usage) float64 {
	info := infoFor(modelSetting)
	if info.InputUSD == 0 && info.OutputUSD == 0 {
		return 0
	}
	cacheRead := info.CacheReadUSD
	if cacheRead == 0 {
		cacheRead = info.InputUSD * cacheReadRate
	}
	inputUSD := float64(u.Input)*info.InputUSD +
		float64(u.CacheRead)*cacheRead +
		float64(u.CacheWrite)*info.InputUSD*cacheWriteRate
	return (inputUSD + float64(u.Output)*info.OutputUSD) / 1_000_000
}

// FormatUSD writes a figure a person can read, "" for nothing worth showing.
func FormatUSD(usd float64) string {
	switch {
	case usd == 0:
		return ""
	case usd < 0.01:
		return "<$0.01"
	default:
		return fmt.Sprintf("$%.2f", usd)
	}
}

// ModelSummary is the one-line description the model picker shows: what it
// holds, what it can be given, and what it charges.
func ModelSummary(modelSetting string) string {
	info := infoFor(modelSetting)

	parts := []string{fmtContext(info.Context) + " context"}
	if info.NoTools {
		parts = append(parts, "no tools")
	}
	if info.InputUSD > 0 || info.OutputUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%s/$%s per Mtok", trimZeros(info.InputUSD), trimZeros(info.OutputUSD)))
	}
	return strings.Join(parts, " · ")
}

func fmtContext(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%dM", tokens/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%dk", tokens/1_000)
	}
	return fmt.Sprint(tokens)
}

// trimZeros writes 0.15 as "0.15" and 15 as "15", so a price reads the way it
// is quoted.
func trimZeros(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
