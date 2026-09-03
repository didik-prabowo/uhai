package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/didik-prabowo/ouhai/internal/provider"
	"github.com/didik-prabowo/ouhai/internal/provider/anthropic"
	"github.com/didik-prabowo/ouhai/internal/provider/openai"
)

// defaultModel is used when both settings.json and the env vars are empty.
// Groq is free and OpenAI-compatible, so ouhai runs as soon as the user has
// a GROQ_API_KEY.
const defaultModel = "groq/llama-3.3-70b-versatile"

// SaveModel stores the chosen model in ~/.ouhai/settings.json. /connect uses
// it so the provider just connected is the one actually used.
func SaveModel(model string) error {
	return save(func(s *Settings) { s.Model = model })
}

// save edits ~/.ouhai/settings.json in place, leaving the fields it does not
// touch alone.
func save(edit func(*Settings)) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".ouhai", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Read the existing file so the other fields survive.
	var s Settings
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("%s is corrupt: %w", path, err)
		}
	}
	edit(&s)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// projectNotesFiles lets a repository state its own conventions once, instead
// of the user repeating them every session. OUHAI.md first, for a project with
// something to say to this agent in particular; AGENTS.md after it, which is
// what repositories write for agents in general — so a project that already
// has one needs nothing added for ouhai to read it.
var projectNotesFiles = []string{"OUHAI.md", "AGENTS.md"}

// CheckCommand is what the project says verifies it, "" when it says nothing.
func CheckCommand() string {
	s, err := LoadSettings()
	if err != nil {
		return ""
	}
	return s.Check
}

// ProjectNotes returns the project's instructions, "" when there are none.
func ProjectNotes() string {
	for _, name := range projectNotesFiles {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if notes := strings.TrimSpace(string(data)); notes != "" {
			return notes
		}
	}
	return ""
}

// Settings is the content of settings.json. Model is a single
// "provider/model" string — one place to change, impossible to get out of
// sync.
type Settings struct {
	Model   string `json:"model"`
	BaseURL string `json:"baseUrl,omitempty"`

	// Permissions decides what each tool may do without being asked; see
	// permission.go for the rule syntax.
	Permissions Permissions `json:"permissions,omitempty"`

	// Check is how this project verifies itself, for /check, when guessing
	// from the files present would get it wrong. Belongs in the project's own
	// .ouhai/settings.json rather than in the home one.
	Check string `json:"check,omitempty"`
}

// settingsFiles returns the settings locations, lowest priority first.
func settingsFiles() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".ouhai", "settings.json"))
	}
	return append(out,
		filepath.Join(".ouhai", "settings.json"),
		filepath.Join(".ouhai", "settings.local.json"),
	)
}

// LoadSettings merges the settings files (later ones win over earlier ones),
// then the env vars, which always win.
func LoadSettings() (Settings, error) {
	var s Settings
	for _, path := range settingsFiles() {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return s, err
		}

		var file Settings
		if err := json.Unmarshal(data, &file); err != nil {
			return s, fmt.Errorf("%s is corrupt: %w", path, err)
		}
		if file.Model != "" {
			s.Model = file.Model
		}
		if file.BaseURL != "" {
			s.BaseURL = file.BaseURL
		}
		if file.Check != "" {
			s.Check = file.Check
		}
		// Appended, not replaced: a project adds rules to what your own
		// settings already say rather than starting the lists over. Denials
		// accumulate, which is the safe direction for a list to grow in.
		s.Permissions.Allow = append(s.Permissions.Allow, file.Permissions.Allow...)
		s.Permissions.Ask = append(s.Permissions.Ask, file.Permissions.Ask...)
		s.Permissions.Deny = append(s.Permissions.Deny, file.Permissions.Deny...)
	}

	if v := strings.TrimSpace(os.Getenv("OUHAI_MODEL")); v != "" {
		s.Model = v
	}
	if v := strings.TrimSpace(os.Getenv("OUHAI_BASE_URL")); v != "" {
		s.BaseURL = v
	}
	if s.Model == "" {
		s.Model = defaultModel
	}
	return s, nil
}

// CurrentModel returns the provider and model in use, split from the single
// "provider/model" setting.
func CurrentModel() (provider, model string) {
	s, err := LoadSettings()
	if err != nil {
		return "", ""
	}
	provider, model, _ = strings.Cut(s.Model, "/")
	return provider, model
}

// LoadProvider assembles a ready-to-use provider from settings plus credentials.
func LoadProvider() (provider.Provider, error) {
	s, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	return loadProvider(s, s.Model)
}

// LoadProviderFor builds a provider for a specific provider/model pair
// without changing the saved active model. It is used by /model to query
// models from every connected provider.
func LoadProviderFor(modelSetting string) (provider.Provider, error) {
	s, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	return loadProvider(s, modelSetting)
}

func loadProvider(s Settings, modelSetting string) (provider.Provider, error) {
	name, model, ok := strings.Cut(modelSetting, "/")
	if !ok || name == "" || model == "" {
		return nil, fmt.Errorf("model %q must look like \"provider/model\", e.g. %q", s.Model, defaultModel)
	}

	baseURL := s.BaseURL
	if baseURL == "" {
		baseURL = providers[name].BaseURL
	}
	if baseURL == "" {
		return nil, fmt.Errorf("unknown provider %q — set \"baseUrl\" in settings.json for a custom endpoint", name)
	}

	key := APIKey(name)
	if key == "" && NeedsKey(name) {
		env := "OUHAI_API_KEY"
		if v := vendorEnv[name][FieldKey]; v != "" {
			env = v
		}
		return nil, fmt.Errorf("no API key for %s yet — type /connect %s, or export %s=...", name, name, env)
	}

	// Anthropic speaks its own wire format; everything else here speaks the
	// OpenAI-style one, whoever hosts it.
	//
	// Do not "return xxx.New(...)" directly: when New fails, its nil pointer
	// gets wrapped into a non-nil interface, and callers checking "p != nil"
	// panic the moment they use it.
	//
	// ponytail: a name compared against a literal. Make it a field of
	// providerInfo when a third wire format arrives, not before.
	if name == "anthropic" {
		c, err := anthropic.New(anthropic.Options{
			Label:     name,
			BaseURL:   baseURL,
			APIKey:    key,
			Model:     model,
			MaxTokens: MaxOutput(modelSetting),
		})
		if err != nil {
			return nil, err
		}
		return c, nil
	}

	c, err := openai.New(openai.Options{
		Label:   name,
		BaseURL: baseURL,
		APIKey:  key,
		Model:   model,
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}
