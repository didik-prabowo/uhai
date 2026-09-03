package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate gives a test a home of its own and takes the environment out of the
// picture: settings read the env last and let it win, so a shell that happens
// to export OUHAI_MODEL would otherwise decide what these tests see. It
// returns the home directory, for tests that write files into it.
func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, env := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_WORKSPACE_ID", "OPENAI_API_KEY", "OUHAI_API_KEY",
		"OUHAI_MODEL", "OUHAI_BASE_URL",
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

	t.Setenv("OUHAI_API_KEY", "dari-ouhai")
	if got := APIKey("anthropic"); got != "dari-ouhai" {
		t.Fatalf("OUHAI_API_KEY should win over everything, got %q", got)
	}
}

func TestSaveModelIsReadBack(t *testing.T) {
	isolate(t)

	if err := SaveModel("groq/" + DefaultModel("groq")); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "groq/llama-3.3-70b-versatile" {
		t.Fatalf("wrong model stored: %q", got.Model)
	}

	// Env still beats the file.
	t.Setenv("OUHAI_MODEL", "ollama/qwen2.5-coder")
	if got, _ := LoadSettings(); got.Model != "ollama/qwen2.5-coder" {
		t.Fatalf("OUHAI_MODEL should win, got %q", got.Model)
	}
}
