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
	"github.com/didik-prabowo/ouhai/internal/provider/openai"
)

// defaultModel dipakai kalau settings.json dan env var dua-duanya kosong.
// Groq gratis dan formatnya OpenAI-compatible, jadi ouhai bisa langsung
// jalan begitu user punya GROQ_API_KEY.
const defaultModel = "groq/llama-3.3-70b-versatile"

// baseURLs adalah endpoint bawaan tiap provider OpenAI-compatible. Provider
// yang gak ada di sini tetap bisa dipakai asal baseUrl diisi di settings.
var baseURLs = map[string]string{
	"groq":       "https://api.groq.com/openai/v1",
	"openai":     "https://api.openai.com/v1",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
	"openrouter": "https://openrouter.ai/api/v1",
	"ollama":     "http://localhost:11434/v1",
}

// noKeyNeeded adalah provider yang jalan lokal, jadi gak butuh API key.
var noKeyNeeded = map[string]bool{"ollama": true}

// Settings adalah isi settings.json. Model ditulis satu string
// "provider/model" — satu tempat buat diganti, gak bisa jadi gak konsisten.
type Settings struct {
	Model   string `json:"model"`
	BaseURL string `json:"baseUrl,omitempty"`
}

// settingsFiles mengembalikan lokasi settings, urut prioritas rendah ke tinggi.
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

// LoadSettings menggabungkan file-file settings (yang belakangan menimpa yang
// duluan), lalu env var yang selalu menang.
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
			return s, fmt.Errorf("%s rusak: %w", path, err)
		}
		if file.Model != "" {
			s.Model = file.Model
		}
		if file.BaseURL != "" {
			s.BaseURL = file.BaseURL
		}
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

// LoadProvider merakit provider siap pakai dari settings + kredensial.
func LoadProvider() (provider.Provider, error) {
	s, err := LoadSettings()
	if err != nil {
		return nil, err
	}

	name, model, ok := strings.Cut(s.Model, "/")
	if !ok || name == "" || model == "" {
		return nil, fmt.Errorf("model %q harus berbentuk \"provider/model\", misal %q", s.Model, defaultModel)
	}

	baseURL := s.BaseURL
	if baseURL == "" {
		baseURL = baseURLs[name]
	}
	if baseURL == "" {
		return nil, fmt.Errorf("provider %q gak dikenal — isi \"baseUrl\" di settings.json kalau endpoint-nya custom", name)
	}

	key := APIKey(name)
	if key == "" && !noKeyNeeded[name] {
		env := "OUHAI_API_KEY"
		if v := vendorEnv[name][FieldKey]; v != "" {
			env = v
		}
		return nil, fmt.Errorf("API key buat %s belum ada — jalankan: export %s=...", name, env)
	}

	return openai.New(openai.Options{
		Label:   name,
		BaseURL: baseURL,
		APIKey:  key,
		Model:   model,
	})
}
