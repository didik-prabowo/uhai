package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate bikin tiap test punya HOME sendiri dan env vendor yang bersih —
// mesin yang kebetulan sudah export ANTHROPIC_* gak boleh bikin test bocor.
func isolate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_WORKSPACE_ID", "OPENAI_API_KEY", "OUHAI_API_KEY"} {
		t.Setenv(env, "")
	}
}

func TestSaveDanBacaKey(t *testing.T) {
	isolate(t)

	if got := APIKey("anthropic"); got != "" {
		t.Fatalf("file belum ada harusnya kosong, dapat %q", got)
	}
	if err := Save("anthropic", Creds{FieldKey: "sk-ant-1"}); err != nil {
		t.Fatal(err)
	}
	if got := APIKey("anthropic"); got != "sk-ant-1" {
		t.Fatalf("dapat %q", got)
	}

	// Provider kedua gak boleh nimpa yang pertama.
	if err := Save("openai", Creds{FieldKey: "sk-oai-1"}); err != nil {
		t.Fatal(err)
	}
	if got := APIKey("anthropic"); got != "sk-ant-1" {
		t.Fatalf("key lama ketimpa: %q", got)
	}
}

func TestIzinFileKetat(t *testing.T) {
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
		t.Fatalf("file kredensial harus 0600, dapat %v", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("folder kredensial harus 0700, dapat %v", di.Mode().Perm())
	}
}

func TestProviderBisaPunyaBeberapaField(t *testing.T) {
	isolate(t)

	// Anthropic tipe identity-linked butuh key + workspace id.
	if err := Save("anthropic", Creds{FieldKey: "sk-ant-1", FieldWorkspace: "wrk-1"}); err != nil {
		t.Fatal(err)
	}
	got := Get("anthropic")
	if got[FieldKey] != "sk-ant-1" || got[FieldWorkspace] != "wrk-1" {
		t.Fatalf("dua field harus kebaca dua-duanya: %v", got)
	}

	// Workspace boleh diisi dari env sendiri, tanpa ganggu key dari file.
	t.Setenv("ANTHROPIC_WORKSPACE_ID", "wrk-env")
	got = Get("anthropic")
	if got[FieldKey] != "sk-ant-1" || got[FieldWorkspace] != "wrk-env" {
		t.Fatalf("env cuma boleh nimpa field-nya sendiri: %v", got)
	}
}

func TestEnvMenangDariFile(t *testing.T) {
	isolate(t)
	if err := Save("anthropic", Creds{FieldKey: "dari-file"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "dari-env")
	if got := APIKey("anthropic"); got != "dari-env" {
		t.Fatalf("env vendor harus menang, dapat %q", got)
	}

	t.Setenv("OUHAI_API_KEY", "dari-ouhai")
	if got := APIKey("anthropic"); got != "dari-ouhai" {
		t.Fatalf("OUHAI_API_KEY harus paling menang, dapat %q", got)
	}
}
