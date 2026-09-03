package config

import "sort"

// providerInfo is everything ouhai knows about one provider. Keeping it in one
// struct means adding a provider is a single entry in the table below, instead
// of remembering to edit a parallel map for the endpoint, the default model,
// the key page and the price.
type providerInfo struct {
	// API is the wire format this provider speaks. Empty means the
	// OpenAI-style one, which most of them do.
	API string

	BaseURL      string // default endpoint, overridable via settings.json
	DefaultModel string // so the user can chat right after /connect
	KeyURL       string // where to get a key, shown by /connect
	Cost         string // rough price bracket, shown in the picker
	Local        bool   // runs on this machine, needs no API key
}

var providers = map[string]providerInfo{
	"anthropic": {
		API:          "anthropic",
		BaseURL:      "https://api.anthropic.com/v1",
		DefaultModel: "claude-sonnet-5",
		KeyURL:       "https://console.anthropic.com/settings/keys",
		Cost:         "paid API",
	},
	"groq": {
		BaseURL:      "https://api.groq.com/openai/v1",
		DefaultModel: "llama-3.3-70b-versatile",
		KeyURL:       "https://console.groq.com/keys",
		Cost:         "free tier",
	},
	"openai": {
		BaseURL:      "https://api.openai.com/v1",
		DefaultModel: "gpt-4o-mini",
		KeyURL:       "https://platform.openai.com/api-keys",
		Cost:         "paid API",
	},
	"gemini": {
		API:          "gemini",
		BaseURL:      "https://generativelanguage.googleapis.com/v1beta",
		DefaultModel: "gemini-2.5-flash",
		KeyURL:       "https://aistudio.google.com/apikey",
		Cost:         "free tier",
	},
	"openrouter": {
		BaseURL:      "https://openrouter.ai/api/v1",
		DefaultModel: "meta-llama/llama-3.3-70b-instruct",
		KeyURL:       "https://openrouter.ai/keys",
		Cost:         "free or paid",
	},
	"ollama": {
		BaseURL:      "http://localhost:11434/v1",
		DefaultModel: "qwen2.5-coder",
		KeyURL:       "https://ollama.com/download",
		Cost:         "free/local",
		Local:        true,
	},
}

// Providers returns the names of providers with a known endpoint, sorted so
// the display stays stable.
func Providers() []string {
	out := make([]string, 0, len(providers))
	for name := range providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ConnectedProviders returns known providers that have credentials available
// or do not need credentials, in stable order.
func ConnectedProviders() []string {
	var out []string
	for _, name := range Providers() {
		if !NeedsKey(name) || APIKey(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

// API is the wire format a provider speaks, "openai" when it has not said
// otherwise — which is most of them, and the reason one client covers Groq,
// OpenRouter, Ollama and OpenAI itself.
func API(provider string) string {
	if api := providers[provider].API; api != "" {
		return api
	}
	return "openai"
}

// Known reports whether a provider has a built-in endpoint.
func Known(provider string) bool { return providers[provider].BaseURL != "" }

// NeedsKey reports whether a provider needs an API key (Ollama does not).
func NeedsKey(provider string) bool { return !providers[provider].Local }

// DefaultModel returns a provider's default model, "" if there is none.
func DefaultModel(provider string) string { return providers[provider].DefaultModel }

// KeyURL returns a provider's key page, "" if unknown.
func KeyURL(provider string) string { return providers[provider].KeyURL }

// Cost describes what a provider charges, for the picker. Unknown providers
// are assumed to be paid: the safer thing to tell someone.
func Cost(provider string) string {
	if c := providers[provider].Cost; c != "" {
		return c
	}
	return "paid API"
}
