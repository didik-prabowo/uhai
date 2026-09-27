package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/didik-prabowo/uhai/internal/tools"
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

// The search tool cannot import this package, so the key reaches it through a
// variable this package sets in init. The wire is the part that breaks
// silently: nothing fails to compile when it is gone, search simply answers
// "no key" to everyone who did not export one.
func TestTheSearchToolIsToldWhereTheKeyIs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(tools.SearchKeyEnv, "")

	if err := Save(SearchProvider, Creds{FieldKey: "BSA-stored"}); err != nil {
		t.Fatal(err)
	}
	if got := tools.SearchKey(); got != "BSA-stored" {
		t.Errorf("the tool read %q, want the key from auth.json", got)
	}

	// The environment still wins, the way it does for every other credential.
	t.Setenv(tools.SearchKeyEnv, "BSA-exported")
	if got := tools.SearchKey(); got != "BSA-exported" {
		t.Errorf("the tool read %q, want the exported one", got)
	}
}

// UHAI_API_KEY fills in every provider's key field here, which is right for a
// gateway and would be a leak for search: the key paying for the conversation
// sent to a search engine that never asked for one.
func TestTheWildcardKeyDoesNotReachTheSearchEngine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(tools.SearchKeyEnv, "")
	t.Setenv("UHAI_API_KEY", "sk-the-expensive-one")

	if got := tools.SearchKey(); got != "" {
		t.Errorf("the tool read %q; the wildcard must not reach it", got)
	}
	// The wildcard still does what it is for.
	if got := APIKey("anthropic"); got != "sk-the-expensive-one" {
		t.Errorf("the wildcard stopped working for a provider: %q", got)
	}
}

// UHAI_API_KEY is read by Get for every provider, so it is uhai's own name and
// has to be kept from the commands run_bash runs. The vendors' variables are
// not in that list on purpose — they are the user's environment and the project
// may need them — which is why this checks one name rather than the table.
func TestUhaiKeyIsHiddenFromCommands(t *testing.T) {
	for _, name := range tools.HiddenEnv {
		if name == "UHAI_API_KEY" {
			return
		}
	}
	t.Error("UHAI_API_KEY is read for every provider and is not in tools.HiddenEnv")
}
