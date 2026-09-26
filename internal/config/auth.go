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

	"github.com/didik-prabowo/uhai/internal/tools"
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
}

// SearchProvider is the entry in auth.json holding the web search key. The
// search tool is in internal/tools, which cannot import this package — the
// arrow already runs the other way — so the lookup is handed to it rather
// than reached for.
const SearchProvider = "brave"

// init teaches the search tool where a stored key lives. In init rather than
// in a startup function because there are four ways into this binary — the
// terminal, the pipe, a worker process, a task — and a wire that has to be
// remembered in each of them is a wire that will be missing from the fourth.
// The failure would be silent in the way this project keeps finding: search
// would work for the person who exported the variable and answer "no key" to
// everyone else.
func init() {
	tools.SearchKey = func() string {
		if v := strings.TrimSpace(os.Getenv(tools.SearchKeyEnv)); v != "" {
			return v
		}
		all, err := LoadAuth()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(all[SearchProvider][FieldKey])
	}
}

// AuthPath returns the credentials file location: ~/.uhai/auth.json.
func AuthPath() (string, error) {
	return InDir("auth.json")
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
// to highest precedence: auth.json, the vendor env vars (OPENAI_API_KEY,
// ANTHROPIC_WORKSPACE_ID, ...), then UHAI_API_KEY. Fields set nowhere are
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
	if v := strings.TrimSpace(os.Getenv("UHAI_API_KEY")); v != "" {
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

// Workspace is the id an identity-linked key acts in, "" when there is none.
// Only Anthropic asks for one so far.
func Workspace(provider string) string { return Get(provider)[FieldWorkspace] }

// EnvKeyVar names the environment variable currently supplying this provider's
// key, "" when none is. The environment wins over the file, so saving a new key
// while one is exported changes nothing — and being told that is the
// difference between a puzzle and a fix.
func EnvKeyVar(provider string) string { return EnvVar(provider, FieldKey) }

// EnvVar names the environment variable currently supplying one field.
func EnvVar(provider, field string) string {
	vars := []string{vendorEnv[provider][field]}
	if field == FieldKey {
		vars = append(vars, "UHAI_API_KEY")
	}
	for _, env := range vars {
		if env != "" && strings.TrimSpace(os.Getenv(env)) != "" {
			return env
		}
	}
	return ""
}

// Save writes (or replaces) one provider's credentials in auth.json without
// touching the others. The file is 0600 and its folder 0700 — credentials
// must not be readable by other users.
// Save merges the fields it is given into what is already stored, rather than
// replacing the entry: the credentials arrive one question at a time, and
// answering the second one must not erase the first.
// Forget removes a provider's saved credentials, which is the half of Save
// that was missing: /connect could put a key in and nothing could take one
// out. It reports whether there was anything to remove, so the caller can say
// "not connected" rather than claiming to have done something.
//
// It does not touch the environment. A GEMINI_API_KEY that is exported still
// wins over the file, so forgetting the file alone would leave the provider
// connected and the message a lie — the caller checks and says so.
func Forget(provider string) (bool, error) {
	all, err := LoadAuth()
	if err != nil {
		return false, err
	}
	if _, ok := all[provider]; !ok {
		return false, nil
	}
	delete(all, provider)

	path, err := AuthPath()
	if err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(data, '\n'), authFilePerm); err != nil {
		return false, err
	}
	return true, os.Chmod(path, authFilePerm)
}

func Save(provider string, c Creds) error {
	if provider == "" || len(c) == 0 {
		return errors.New("a provider and at least one credential are needed")
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
	if all[provider] == nil {
		all[provider] = Creds{}
	}
	for field, value := range c {
		if value = strings.TrimSpace(value); value != "" {
			all[provider][field] = value
		}
	}
	if all[provider][FieldKey] == "" {
		return errors.New("the \"key\" field must not be empty")
	}

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
