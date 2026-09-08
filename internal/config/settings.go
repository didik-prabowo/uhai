package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/provider/anthropic"
	"github.com/didik-prabowo/uhai/internal/provider/openai"
)

// defaultModel is used when both settings.json and the env vars are empty.
// It has been three things now — Groq, then Gemini for its free tier, and
// neither is a provider uhai ships any more. There is no free one left to
// point at, so this is the one worth paying for rather than the one that
// costs least: a first turn that answers well beats a first turn that is free
// and wrong, and /connect is one command away for anyone who disagrees.
const defaultModel = "anthropic/claude-sonnet-5"

// SaveModel stores the chosen model in ~/.uhai/settings.json. /connect uses
// it so the provider just connected is the one actually used.
func SaveModel(model string) error {
	return save(func(s *Settings) { s.Model = model })
}

// SetSkillOff switches a skill off, or back on, in the *project's*
// settings.json rather than the home one. That is the granularity the choice
// has: a skill you carry everywhere is wanted in some repositories and not
// others, and writing it home would turn one project's answer into every
// project's. Switching one off everywhere stays a hand edit of ~/.uhai.
//
// Off accumulates across the two files, so switching one back on here cannot
// undo a home settings.json that turned it off — the caller reads the state
// back rather than assuming the write decided it.
func SetSkillOff(name string, off bool) error {
	return saveProject(func(s *Settings) {
		out := s.SkillsOff[:0]
		for _, have := range s.SkillsOff {
			if have != name {
				out = append(out, have)
			}
		}
		s.SkillsOff = out
		if off {
			s.SkillsOff = append(s.SkillsOff, name)
		}
	})
}

// saveProject edits .uhai/settings.json beside the code, creating it if the
// project has none. It is save's twin, pointed at the project rather than the
// home directory.
func saveProject(edit func(*Settings)) error {
	path := filepath.Join(ProjectDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Read what is there so the other fields survive.
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

// save edits ~/.uhai/settings.json in place, leaving the fields it does not
// touch alone.
func save(edit func(*Settings)) error {
	path, err := InDir("settings.json")
	if err != nil {
		return err
	}
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
// of the user repeating them every session. The first one found is used.
//
// Each name comes in a ".local" variant, which is a convention rather than
// anything the AGENTS.md standard says: an untracked file for how one person
// works, usually importing the shared one with @. It is read first for that
// reason — it is the more specific of the two, and it knows how to pull the
// other in.
var projectNotesFiles = []string{
	"UHAI.local.md",
	"UHAI.md",
	"AGENTS.local.md",
	"AGENTS.md",
	"CLAUDE.local.md",
	"CLAUDE.md",
}

// importDepth is how far a chain of @ lines is followed. Notes importing notes
// importing notes is a shape worth allowing and not worth chasing.
const importDepth = 3

// expandImports replaces a line that is only "@path" with the file it names —
// the way project notes are written when one file is shared and another is
// personal. A file that cannot be read, or that has already been pulled in,
// leaves its line as it stands: better a visible "@thing" than a silent gap.
func expandImports(notes, from string, seen map[string]bool, depth int) string {
	if depth >= importDepth {
		return notes
	}

	lines := strings.Split(notes, "\n")
	for i, line := range lines {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "@")
		if !ok || path == "" || strings.ContainsAny(path, " \t") {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(from), path)
		}
		if seen[path] {
			lines[i] = ""
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		seen[path] = true
		lines[i] = expandImports(strings.TrimSpace(string(data)), path, seen, depth+1)
	}
	return strings.Join(lines, "\n")
}

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
			return expandImports(notes, name, map[string]bool{name: true}, 0)
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

	// BaseURLs points one provider somewhere else, which "baseUrl" cannot do:
	// that one applies to whichever provider is loaded, so aiming it at a
	// proxy for one vendor silently redirects the next /model too. Z.ai's
	// coding plan is the case that found this — the same API and key at
	// another address, wanted for that provider and no other.
	BaseURLs map[string]string `json:"baseUrls,omitempty"`

	// Permissions decides what each tool may do without being asked; see
	// permission.go for the rule syntax.
	Permissions Permissions `json:"permissions,omitempty"`

	// SkillDirs are extra folders to look for skills in, for a project that
	// keeps them somewhere other than .uhai, .claude or .agents.
	SkillDirs []string `json:"skills,omitempty"`

	// SkillsOff names skills to leave out of the prompt. A skill costs its
	// line on every request whether it is ever opened or not, so the ones you
	// carry everywhere and need in one project out of ten are worth switching
	// off — in the project's settings, or in your own for everywhere.
	SkillsOff []string `json:"skillsOff,omitempty"`

	// Check is how this project verifies itself, for /check, when guessing
	// from the files present would get it wrong. Belongs in the project's own
	// .uhai/settings.json rather than in the home one.
	Check string `json:"check,omitempty"`

	// Effort is how hard the model should think — "low" through "max" — for
	// the models that take it. Empty takes the table's answer, which is what
	// the vendor recommends for this kind of work. It is here because it is
	// the one quality setting with a bill attached: thinking harder is worth
	// paying for on a plan and not on "what does this function do".
	Effort string `json:"effort,omitempty"`
}

// settingsFiles returns the settings locations, lowest priority first.
func settingsFiles() []string {
	var out []string
	if path, err := InDir("settings.json"); err == nil {
		out = append(out, path)
	}
	return append(out,
		filepath.Join(ProjectDir, "settings.json"),
		filepath.Join(ProjectDir, "settings.local.json"),
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
		for name, url := range file.BaseURLs {
			if s.BaseURLs == nil {
				s.BaseURLs = map[string]string{}
			}
			s.BaseURLs[name] = url // the nearer file wins, provider by provider
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
		s.SkillDirs = append(s.SkillDirs, file.SkillDirs...)
		// Off accumulates the same way a denial does: a project can switch off
		// one of yours, and cannot switch on what you turned off for yourself.
		s.SkillsOff = append(s.SkillsOff, file.SkillsOff...)
	}

	if v := strings.TrimSpace(os.Getenv("UHAI_MODEL")); v != "" {
		s.Model = v
	}
	if v := strings.TrimSpace(os.Getenv("UHAI_BASE_URL")); v != "" {
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

// SaveBaseURL points a provider at an endpoint, in ~/.uhai/settings.json. It
// is what /connect writes for a custom one, and the reason that flow needs no
// file editing: a provider uhai has never heard of is a base URL and a key,
// and both now have a place to be typed.
func SaveBaseURL(provider, url string) error {
	return save(func(s *Settings) {
		if s.BaseURLs == nil {
			s.BaseURLs = map[string]string{}
		}
		s.BaseURLs[provider] = url
	})
}

// ForgetBaseURL removes one, so /disconnect can take a custom provider out
// whole rather than leaving a half of it behind that /model still offers.
func ForgetBaseURL(provider string) error {
	return save(func(s *Settings) { delete(s.BaseURLs, provider) })
}

// CustomProviders are the ones defined only by a baseUrl in settings — a
// gateway, a company endpoint, somebody's proxy. Sorted, and never including a
// name the table already has: pointing "anthropic" elsewhere is an override of
// a known provider, not a new one.
func CustomProviders() []string {
	s, err := LoadSettings()
	if err != nil {
		return nil
	}
	var out []string
	for name := range s.BaseURLs {
		if name != "" && !Known(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ListerModel is a model name to build a client with when the point is to ask
// that client what models exist. The provider's default when it has one, then
// whatever is selected if it belongs to this provider, and failing both a
// placeholder — listing does not send the model anywhere, and a custom
// provider has no default by definition. Without this the model picker simply
// skipped every gateway, which made adding one only half useful.
func ListerModel(provider string) string {
	if m := DefaultModel(provider); m != "" {
		return m
	}
	if s, err := LoadSettings(); err == nil {
		if name, model, ok := strings.Cut(s.Model, "/"); ok && name == provider {
			return model
		}
	}
	return "-"
}

// BaseURLOf is where a provider points, "" when nothing says. For the picker,
// which shows it beside a custom provider: two gateways look identical
// otherwise, and the endpoint is the whole difference between them.
func BaseURLOf(provider string) string {
	s, err := LoadSettings()
	if err != nil {
		return providers[provider].BaseURL
	}
	return providerURL(s, provider)
}

// providerURL is where one provider's requests go: its own override first
// since that is the specific answer, then the global one — which is there for
// somebody running a single endpoint for everything — then the table.
func providerURL(s Settings, name string) string {
	if url := s.BaseURLs[name]; url != "" {
		return url
	}
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return providers[name].BaseURL
}

func loadProvider(s Settings, modelSetting string) (provider.Provider, error) {
	name, model, ok := strings.Cut(modelSetting, "/")
	if !ok || name == "" || model == "" {
		return nil, fmt.Errorf("model %q must look like \"provider/model\", e.g. %q", s.Model, defaultModel)
	}

	baseURL := providerURL(s, name)
	if baseURL == "" {
		return nil, fmt.Errorf("unknown provider %q — set \"baseUrl\" in settings.json for a custom endpoint", name)
	}

	key := APIKey(name)
	if key == "" && NeedsKey(name) {
		env := "UHAI_API_KEY"
		if v := vendorEnv[name][FieldKey]; v != "" {
			env = v
		}
		return nil, fmt.Errorf("no API key for %s yet — type /connect %s, or export %s=...", name, name, env)
	}

	// Which client to build is a property of the provider, like its endpoint
	// and its default model — a table entry rather than a name compared
	// against a literal, now that there are three formats to choose from.
	//
	// Do not "return xxx.New(...)" directly: when New fails, its nil pointer
	// gets wrapped into a non-nil interface, and callers checking "p != nil"
	// panic the moment they use it.
	switch API(name) {
	case "anthropic":
		c, err := anthropic.New(anthropic.Options{
			Label:       name,
			BaseURL:     baseURL,
			APIKey:      key,
			Model:       model,
			MaxTokens:   MaxOutput(modelSetting),
			WorkspaceID: Workspace(name),
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
