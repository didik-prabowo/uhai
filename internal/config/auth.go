// Package config nyimpen kredensial provider. API key sengaja dipisah dari
// setting biasa dan ditulis dengan izin ketat — bentuknya ikut opencode: satu
// file map provider -> kredensial, mode 0600. Nilainya map, bukan string,
// karena sebagian provider butuh lebih dari satu nilai (Anthropic tipe
// identity-linked butuh key + workspace id).
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

// Field baku di dalam Creds. Provider boleh nambah field lain sendiri.
const (
	FieldKey       = "key"
	FieldWorkspace = "workspace"
)

// Creds adalah kredensial satu provider: field -> nilai.
type Creds map[string]string

// vendorEnv adalah env var baku tiap vendor, per field. Dibaca setelah file,
// jadi env menang — biar orang yang env-nya udah keisi langsung jalan.
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

// AuthPath mengembalikan lokasi file kredensial: ~/.ouhai/auth.json.
func AuthPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ouhai", "auth.json"), nil
}

// LoadAuth membaca seluruh isi auth.json. File yang belum ada bukan error —
// balikin map kosong, biar pemanggil bisa langsung nambah entri.
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
		return nil, fmt.Errorf("%s rusak: %w", path, err)
	}
	return all, nil
}

// Get mengembalikan kredensial satu provider, hasil gabungan file + env.
// Urutan menang, dari rendah ke tinggi: auth.json, env var vendor
// (ANTHROPIC_API_KEY, ANTHROPIC_WORKSPACE_ID, dst), lalu OUHAI_API_KEY.
// Field yang gak keisi di mana-mana ya gak ada di map hasilnya.
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

// APIKey pintasan buat field yang paling sering dipakai.
func APIKey(provider string) string { return Get(provider)[FieldKey] }

// Save nyimpen (atau nimpa) kredensial satu provider ke auth.json. Provider
// lain gak keganggu. File ditulis 0600 dan foldernya 0700 — kredensial gak
// boleh kebaca user lain.
func Save(provider string, c Creds) error {
	if provider == "" || c[FieldKey] == "" {
		return errors.New("provider dan field \"key\" gak boleh kosong")
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
	// WriteFile gak nurunin izin file yang sudah ada, jadi dipaksa lagi
	// pakai Chmod — file lama bisa saja kadung 0644.
	if err := os.WriteFile(path, append(data, '\n'), authFilePerm); err != nil {
		return err
	}
	return os.Chmod(path, authFilePerm)
}
