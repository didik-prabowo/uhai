package config

import "sort"

// providerInfo is everything uhai knows about one provider. Keeping it in one
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

	// Extra names credentials beyond the key that /connect should ask for.
	// Anthropic's identity-linked keys act inside a workspace, and the API
	// refuses the request without its id.
	Extra []string
}

var providers = map[string]providerInfo{
	"anthropic": {
		API:          "anthropic",
		BaseURL:      "https://api.anthropic.com/v1",
		DefaultModel: "claude-sonnet-5",
		KeyURL:       "https://console.anthropic.com/settings/keys",
		Cost:         "paid API",
		Extra:        []string{FieldWorkspace},
	},
	"openai": {
		BaseURL:      "https://api.openai.com/v1",
		DefaultModel: "gpt-4o-mini",
		KeyURL:       "https://platform.openai.com/api-keys",
		Cost:         "paid API",
	},
}

// Providers returns every provider that can be chosen: the ones settings
// define with nothing but a baseUrl, then the ones with a table entry. Each
// half sorted.
//
// Custom first, and that order was the other way round to begin with — the
// argument being that a gateway added last week should not push the vendors
// about. It was wrong for the case that actually happens: going to the trouble
// of registering an endpoint is a statement that it is the one being used, and
// the model picker shows six rows per provider, so three built-ins put a
// gateway's models on row nineteen. The reason it exists is the reason it goes
// on top.
//
// Custom ones are here rather than in a list of their own because the pickers
// and /disconnect all read this: a provider that can be connected and cannot
// be found again is half a feature.
func Providers() []string {
	built := make([]string, 0, len(providers))
	for name := range providers {
		built = append(built, name)
	}
	sort.Strings(built)
	return append(CustomProviders(), built...)
}

// Configured reports whether a provider can be loaded at all — a table entry,
// or a baseUrl someone wrote for it. Distinct from Known on purpose: Known
// means "uhai ships an endpoint for this", which is what the model table asks
// when deciding whether it may quote a price. A gateway is Configured and not
// Known, and both answers are the true ones.
func Configured(provider string) bool {
	if Known(provider) {
		return true
	}
	s, err := LoadSettings()
	return err == nil && s.BaseURLs[provider] != ""
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
// otherwise — which is most of them, and the reason one client covers
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

// ExtraFields is what /connect asks for after the key, in order. Empty for
// every provider whose key is the whole story.
func ExtraFields(provider string) []string { return providers[provider].Extra }
