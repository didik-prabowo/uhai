package config

import (
	"fmt"
	"strings"
)

// What is known about a model: what it can hold, how much it may write, what
// it can be given, and what it charges. Every field is here because something
// asks for it — the agent compacts against Context, the Messages API refuses a
// request without MaxOutput, the model picker shows the rest, and the status
// row prices the turn.
//
// Prices are list prices in US dollars per million tokens, and only for models
// sold by the vendor that made them. The same open model costs different money
// at Groq, OpenRouter or a machine under the desk, so those are left at zero
// and simply not priced: no figure is better than a confident wrong one.
type modelInfo struct {
	Context   int
	MaxOutput int

	InputUSD  float64 // per million tokens, 0 when it depends on the host
	OutputUSD float64

	// NoTools marks the rare model that cannot be given tools, so the zero
	// value is the common case: it can.
	NoTools bool
	Vision  bool
}

const (
	defaultContext   = 32_000
	defaultMaxOutput = 4_096
)

// models is matched by prefix against the model's own name, so a family is one
// entry rather than one per release. The longest match wins, which is what
// lets a specific model correct its family.
//
// ponytail: a hand-written table, and list prices drift. Providers that report
// their own limits could fill it in, but none of the ones here do it the same
// way, and what is unlisted still works — quietly, at safe defaults.
var models = map[string]modelInfo{
	"claude-opus":   {Context: 200_000, MaxOutput: 8_192, InputUSD: 15, OutputUSD: 75, Vision: true},
	"claude-sonnet": {Context: 200_000, MaxOutput: 8_192, InputUSD: 3, OutputUSD: 15, Vision: true},
	"claude-haiku":  {Context: 200_000, MaxOutput: 8_192, InputUSD: 1, OutputUSD: 5, Vision: true},
	"claude-":       {Context: 200_000, MaxOutput: 8_192, Vision: true},

	"gpt-4o-mini": {Context: 128_000, MaxOutput: 16_384, InputUSD: 0.15, OutputUSD: 0.60, Vision: true},
	"gpt-4o":      {Context: 128_000, MaxOutput: 16_384, InputUSD: 2.50, OutputUSD: 10, Vision: true},
	"gpt-4.1":     {Context: 128_000, MaxOutput: 16_384, Vision: true},

	"gemini-2.5": {Context: 1_000_000, MaxOutput: 8_192, Vision: true},
	"gemini-2.0": {Context: 1_000_000, MaxOutput: 8_192, Vision: true},

	// GLM, from Z.ai, which makes and sells them — so they are priced. The
	// pricing page publishes no context windows: 128k is the figure that is
	// safe to be wrong about, and 4.6 is the one Z.ai documents at 200k.
	// glm-4.7-flash is free and unpriced, which is also where -flashx lands —
	// no figure beats a confident wrong one.
	"glm-5.3-flash": {Context: 128_000, MaxOutput: 8_192, InputUSD: 0.075, OutputUSD: 0.25},
	"glm-5.3":       {Context: 128_000, MaxOutput: 8_192, InputUSD: 1.40, OutputUSD: 4.40},
	"glm-4.7-flash": {Context: 128_000, MaxOutput: 8_192},
	"glm-4.7":       {Context: 128_000, MaxOutput: 8_192, InputUSD: 0.60, OutputUSD: 2.20},
	"glm-4.6":       {Context: 200_000, MaxOutput: 8_192, InputUSD: 0.60, OutputUSD: 2.20},
	"glm-":          {Context: 128_000, MaxOutput: 8_192},

	// Open models: hosted by everyone, priced by each host, so no figures.
	"llama-3.3":     {Context: 128_000, MaxOutput: 8_192},
	"llama-3.1":     {Context: 128_000, MaxOutput: 8_192},
	"qwen2.5-coder": {Context: 32_768, MaxOutput: 4_096},
}

// infoFor looks up "provider/model", or the bare model name. A model name may
// itself carry slashes — "openrouter/meta-llama/llama-3.3-70b-instruct" — so
// the match is made on the last segment, which is where the family lives.
func infoFor(modelSetting string) modelInfo {
	name := modelSetting
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)

	best := modelInfo{Context: defaultContext, MaxOutput: defaultMaxOutput}
	longest := 0
	for prefix, info := range models {
		if strings.HasPrefix(name, prefix) && len(prefix) > longest {
			best, longest = info, len(prefix)
		}
	}
	return best
}

// ContextWindow is how much history a model will take, in tokens. The agent
// compacts before reaching it.
func ContextWindow(modelSetting string) int { return infoFor(modelSetting).Context }

// MaxOutput is the longest answer to ask for. Anthropic requires it; the
// OpenAI-style endpoints are left to their own defaults, which is what they
// were doing before there was a table to ask.
func MaxOutput(modelSetting string) int { return infoFor(modelSetting).MaxOutput }

// SupportsTools reports whether the model can be given tools. Unknown models
// are assumed to manage: nearly all do, and refusing to try would be worse
// than the error the provider returns.
func SupportsTools(modelSetting string) bool { return !infoFor(modelSetting).NoTools }

// SupportsVision reports whether the model reads images. Unknown models are
// assumed not to, which is the way round that cannot waste a request.
func SupportsVision(modelSetting string) bool { return infoFor(modelSetting).Vision }

// CostUSD prices one turn, "" when the model's price depends on who is hosting
// it. Rounded to something a person can read rather than to the cent, since
// a turn often costs less than one.
func CostUSD(modelSetting string, inputTokens, outputTokens int) string {
	info := infoFor(modelSetting)
	if info.InputUSD == 0 && info.OutputUSD == 0 {
		return ""
	}
	usd := (float64(inputTokens)*info.InputUSD + float64(outputTokens)*info.OutputUSD) / 1_000_000
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
	if info.Vision {
		parts = append(parts, "images")
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
