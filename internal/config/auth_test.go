package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate gives a test a home of its own and takes the environment out of the
// picture: settings read the env last and let it win, so a shell that happens
// to export UHAI_MODEL would otherwise decide what these tests see. It
// returns the home directory, for tests that write files into it.
func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, env := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_WORKSPACE_ID", "OPENAI_API_KEY", "UHAI_API_KEY",
		"UHAI_MODEL", "UHAI_BASE_URL",
	} {
		t.Setenv(env, "")
	}
	return home
}

func TestSaveAndReadKey(t *testing.T) {
	isolate(t)

	if got := APIKey("anthropic"); got != "" {
		t.Fatalf("no file yet should mean empty, got %q", got)
	}
	if err := Save("anthropic", Creds{FieldKey: "sk-ant-1"}); err != nil {
		t.Fatal(err)
	}
	if got := APIKey("anthropic"); got != "sk-ant-1" {
		t.Fatalf("dapat %q", got)
	}

	// A second provider must not overwrite the first.
	if err := Save("openai", Creds{FieldKey: "sk-oai-1"}); err != nil {
		t.Fatal(err)
	}
	if got := APIKey("anthropic"); got != "sk-ant-1" {
		t.Fatalf("the old key was overwritten: %q", got)
	}
}

func TestFilePermissionsAreTight(t *testing.T) {
	isolate(t)
	if err := Save("anthropic", Creds{FieldKey: "sk-ant-1"}); err != nil {
		t.Fatal(err)
	}

	path, _ := AuthPath()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the credentials file must be 0600, got %v", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("the credentials folder must be 0700, got %v", di.Mode().Perm())
	}
}

func TestProviderCanHaveSeveralFields(t *testing.T) {
	isolate(t)

	// Identity-linked Anthropic keys need a key plus a workspace id.
	if err := Save("anthropic", Creds{FieldKey: "sk-ant-1", FieldWorkspace: "wrk-1"}); err != nil {
		t.Fatal(err)
	}
	got := Get("anthropic")
	if got[FieldKey] != "sk-ant-1" || got[FieldWorkspace] != "wrk-1" {
		t.Fatalf("both fields should be readable: %v", got)
	}

	// Workspace may come from its own env var without touching the file key.
	t.Setenv("ANTHROPIC_WORKSPACE_ID", "wrk-env")
	got = Get("anthropic")
	if got[FieldKey] != "sk-ant-1" || got[FieldWorkspace] != "wrk-env" {
		t.Fatalf("env must only override its own field: %v", got)
	}
}

func TestEnvBeatsFile(t *testing.T) {
	isolate(t)
	if err := Save("anthropic", Creds{FieldKey: "dari-file"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "dari-env")
	if got := APIKey("anthropic"); got != "dari-env" {
		t.Fatalf("the vendor env var should win, got %q", got)
	}

	t.Setenv("UHAI_API_KEY", "dari-uhai")
	if got := APIKey("anthropic"); got != "dari-uhai" {
		t.Fatalf("UHAI_API_KEY should win over everything, got %q", got)
	}
}

func TestSaveModelIsReadBack(t *testing.T) {
	isolate(t)

	if err := SaveModel("openai/" + DefaultModel("openai")); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/gpt-4o-mini" {
		t.Fatalf("wrong model stored: %q", got.Model)
	}

	// Env still beats the file.
	t.Setenv("UHAI_MODEL", "ollama/qwen2.5-coder")
	if got, _ := LoadSettings(); got.Model != "ollama/qwen2.5-coder" {
		t.Fatalf("UHAI_MODEL should win, got %q", got.Model)
	}
}

// Credentials arrive one question at a time, so saving the second field must
// not erase the first.
func TestSaveMergesFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_WORKSPACE_ID", "UHAI_API_KEY"} {
		t.Setenv(env, "")
	}

	if err := Save("anthropic", Creds{FieldKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := Save("anthropic", Creds{FieldWorkspace: "wrkspc_1"}); err != nil {
		t.Fatal(err)
	}
	if key, ws := APIKey("anthropic"), Workspace("anthropic"); key != "k" || ws != "wrkspc_1" {
		t.Fatalf("key=%q workspace=%q — one write erased the other", key, ws)
	}

	// A workspace without a key is not a connection.
	if err := Save("openai", Creds{FieldWorkspace: "w"}); err == nil {
		t.Error("saving credentials with no key must be refused")
	}
}
