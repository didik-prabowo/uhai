// Package config stores provider credentials and settings. API keys are kept
// apart from ordinary settings and written with tight permissions — the shape
// follows opencode: one file mapping provider -> credentials, mode 0600. The
// value is a map, not a string, because some providers need more than one
// value (identity-linked Anthropic keys need a key plus a workspace id).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	authDirPerm  fs.FileMode = 0o700
	authFilePerm fs.FileMode = 0o600
)

// Standard fields inside Creds. Providers may add their own.
const (
	FieldKey       = "key"
	FieldWorkspace = "workspace"
)

// Creds holds one provider's credentials: field -> value.
type Creds map[string]string

// vendorEnv lists each vendor's standard env vars, per field. Read after the
// file so env wins — someone whose env is already set works out of the box.
var vendorEnv = map[string]map[string]string{
	"anthropic": {
		FieldKey:       "ANTHROPIC_API_KEY",
		FieldWorkspace: "ANTHROPIC_WORKSPACE_ID",
	},
	"openai": {
		FieldKey: "OPENAI_API_KEY",
	},
	"groq": {
		FieldKey: "GROQ_API_KEY",
	},
	"gemini": {
		FieldKey: "GEMINI_API_KEY",
	},
	"openrouter": {
		FieldKey: "OPENROUTER_API_KEY",
	},
}

// AuthPath returns the credentials file location: ~/.ouhai/auth.json.
func AuthPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ouhai", "auth.json"), nil
}

// LoadAuth reads all of auth.json. A missing file is not an error — it
// returns an empty map so callers can add entries right away.
func LoadAuth() (map[string]Creds, error) {
	path, err := AuthPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Creds{}, nil
	}
	if err != nil {
		return nil, err
	}

	all := map[string]Creds{}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", path, err)
	}
	return all, nil
}

// Get returns one provider's credentials, merged from file and env. Lowest
// to highest precedence: auth.json, the vendor env vars (GROQ_API_KEY,
// ANTHROPIC_WORKSPACE_ID, ...), then OUHAI_API_KEY. Fields set nowhere are
// simply absent from the result.
func Get(provider string) Creds {
	out := Creds{}
	if all, err := LoadAuth(); err == nil {
		for k, v := range all[provider] {
			out[k] = strings.TrimSpace(v)
		}
	}
	for field, env := range vendorEnv[provider] {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			out[field] = v
		}
	}
	if v := strings.TrimSpace(os.Getenv("OUHAI_API_KEY")); v != "" {
		out[FieldKey] = v
	}

	for field, v := range out {
		if v == "" {
			delete(out, field)
		}
	}
	return out
}

// APIKey is a shortcut for the field used most often.
func APIKey(provider string) string { return Get(provider)[FieldKey] }

// EnvKeyVar names the environment variable currently supplying this provider's
// key, "" when none is. The environment wins over the file, so saving a new key
// while one is exported changes nothing — and being told that is the
// difference between a puzzle and a fix.
func EnvKeyVar(provider string) string {
	for _, env := range []string{vendorEnv[provider][FieldKey], "OUHAI_API_KEY"} {
		if env != "" && strings.TrimSpace(os.Getenv(env)) != "" {
			return env
		}
	}
	return ""
}

// Save writes (or replaces) one provider's credentials in auth.json without
// touching the others. The file is 0600 and its folder 0700 — credentials
// must not be readable by other users.
func Save(provider string, c Creds) error {
	if provider == "" || c[FieldKey] == "" {
		return errors.New("provider and the \"key\" field must not be empty")
	}

	path, err := AuthPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), authDirPerm); err != nil {
		return err
	}

	all, err := LoadAuth()
	if err != nil {
		return err
	}
	all[provider] = c

	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	// WriteFile does not lower the permissions of an existing file, so Chmod
	// forces them — an older file may already be 0644.
	if err := os.WriteFile(path, append(data, '\n'), authFilePerm); err != nil {
		return err
	}
	return os.Chmod(path, authFilePerm)
}
