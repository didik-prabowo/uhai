package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// timeAfter keeps the waits in this file readable in milliseconds.
func timeAfter(ms int) <-chan time.Time { return time.After(time.Duration(ms) * time.Millisecond) }

// docsPath is the page these tests hold to the code. A tool the model can call
// and nobody documented is how a surprise gets shipped.
const docsPath = "../../docs/tools.md"

func docs(t *testing.T) string {
	t.Helper()

	text, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("the tools page must exist: %v", err)
	}
	return string(text)
}

// Every tool is documented, and everything documented is a tool. Both
// directions matter: the first catches a capability nobody wrote down, the
// second catches a page describing something that was removed.
func TestDocumentedToolsMatchTheCode(t *testing.T) {
	page := docs(t)

	defined := map[string]bool{}
	for _, spec := range Definitions() {
		defined[spec.Name] = true
		if !strings.Contains(page, "### "+spec.Name) {
			t.Errorf("%s is offered to the model but has no section in docs/tools.md", spec.Name)
		}
	}

	// The sections named after tools are the ones whose heading looks like an
	// identifier; the prose ones ("Configure", "Internals") do not.
	for _, m := range regexp.MustCompile(`(?m)^### ([a-z_]+)$`).FindAllStringSubmatch(page, -1) {
		if !defined[m[1]] {
			t.Errorf("docs/tools.md documents %q, which no longer exists", m[1])
		}
	}
}

// The defaults table on that page is the security claim this project makes: it
// says which tools act without asking. It has to agree with the code that
// enforces it, or it is worse than nothing.
func TestDocumentedDefaultsMatchTheCode(t *testing.T) {
	page := docs(t)

	rows := regexp.MustCompile("(?m)^\\| `(allow|ask)` \\| (.+) \\|$")
	links := regexp.MustCompile("`(\\w+)`")

	found := map[string]bool{}
	for _, row := range rows.FindAllStringSubmatch(page, -1) {
		asks := row[1] == "ask"
		for _, link := range links.FindAllStringSubmatch(row[2], -1) {
			name := link[1]
			found[name] = true
			if asks != NeedsConfirm(name) {
				t.Errorf("docs put %s under %q, the code says NeedsConfirm=%v", name, row[1], NeedsConfirm(name))
			}
		}
	}
	for _, spec := range Definitions() {
		if !found[spec.Name] {
			t.Errorf("%s is in neither default column", spec.Name)
		}
	}
}

// Writing to disk and running commands ask; looking does not. This is the one
// invariant the whole permission model rests on.
func TestOnlyChangingToolsAskFirst(t *testing.T) {
	asks := map[string]bool{
		"read_file":  false,
		"glob":       false,
		"grep":       false,
		"write_file": true,
		"edit_file":  true,
		"run_bash":   true,
		"fetch_url":  true,
	}
	for name, want := range asks {
		if got := NeedsConfirm(name); got != want {
			t.Errorf("NeedsConfirm(%q) = %v, want %v", name, got, want)
		}
	}
	if NeedsConfirm("something_invented") {
		t.Error("an unknown tool must not be treated as one that asks — it must not run at all")
	}
}

// The schemas are the model's only instructions for calling a tool: a required
// field the model cannot see is a call it will get wrong every time.
func TestSchemasAreValidAndRequireWhatIsUsed(t *testing.T) {
	required := map[string][]string{
		"read_file":  {"path"},
		"write_file": {"path", "content"},
		"edit_file":  {"path", "old", "new"},
		"glob":       {"pattern"},
		"fetch_url":  {"url"},
		"grep":       {"pattern"},
		"run_bash":   {"command"},
	}

	for _, spec := range Definitions() {
		if spec.Description == "" {
			t.Errorf("%s has no description", spec.Name)
		}

		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(spec.JSONSchema, &schema); err != nil {
			t.Errorf("%s has an unparsable schema: %v", spec.Name, err)
			continue
		}
		if schema.Type != "object" {
			t.Errorf("%s takes %q, want an object", spec.Name, schema.Type)
		}
		if got := strings.Join(schema.Required, ","); got != strings.Join(required[spec.Name], ",") {
			t.Errorf("%s requires %q, want %q", spec.Name, got, required[spec.Name])
		}
		for _, field := range schema.Required {
			if _, ok := schema.Properties[field]; !ok {
				t.Errorf("%s requires %q without describing it", spec.Name, field)
			}
		}
	}
}

// A name nobody implements must fail loudly. Answering "" would read to the
// model as a tool that did nothing successfully.
func TestUnknownToolIsRefused(t *testing.T) {
	out, isErr := Execute(context.Background(), "delete_everything", json.RawMessage(`{}`))
	if !isErr || !strings.Contains(out, "unknown tool") {
		t.Fatalf("got %q, isError=%v", out, isErr)
	}
}

// A result is cut at the end, so what a command printed last — where a failure
// explains itself — is what survives.
func TestLongResultsAreTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxResultLen*2)), 0o644); err != nil {
		t.Fatal(err)
	}

	out, isErr := Execute(context.Background(), "read_file", json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
	if isErr {
		t.Fatalf("reading a large file is not an error: %q", out)
	}
	if !strings.HasSuffix(out, "...[output truncated]") {
		t.Fatal("a truncated result must say so, or the model treats half a file as the whole one")
	}
	if len(out) > maxResultLen+len("\n...[output truncated]") {
		t.Fatalf("result is %d characters, cap is %d", len(out), maxResultLen)
	}
}

// Reading what is not there is an error the model can act on, not an empty
// string it would take for an empty file.
func TestReadingWhatIsNotThereIsAnError(t *testing.T) {
	out, isErr := Execute(context.Background(), "read_file", json.RawMessage(`{"path":"/nowhere/at/all.go"}`))
	if !isErr || !strings.Contains(out, "could not read file") {
		t.Fatalf("got %q, isError=%v", out, isErr)
	}
}

// The three refusals of edit_file, each of which prevents a specific way of
// quietly corrupting a file.
func TestEditRefusesWhatItCannotDoSafely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("satu\ndua\nsatu\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, input, want string
	}{
		{"empty old", `{"path":%q,"old":"","new":"x"}`, "use write_file"},
		{"missing", `{"path":%q,"old":"tiga","new":"x"}`, "not in the file"},
		{"ambiguous", `{"path":%q,"old":"satu","new":"x"}`, "appears 2 times"},
	} {
		out, isErr := Execute(context.Background(), "edit_file", json.RawMessage(fmt.Sprintf(c.input, path)))
		if !isErr || !strings.Contains(out, c.want) {
			t.Errorf("%s: got %q, want an error mentioning %q", c.name, out, c.want)
		}
	}

	// The file is left exactly as it was.
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "satu\ndua\nsatu\n" {
		t.Fatalf("a refused edit must change nothing, file is now %q", after)
	}
}

// Directories that are huge, generated, or not the user's code are never
// walked — searching them wastes the result budget on noise.
func TestSearchesSkipTheNoiseDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"src/main.go", ".git/config.go", "node_modules/pkg/index.go", "vendor/lib/lib.go"} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x // penanda\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, tool := range []struct{ name, input string }{
		{"glob", `{"pattern":"**/*.go","path":%q}`},
		{"grep", `{"pattern":"penanda","path":%q}`},
	} {
		out, isErr := Execute(context.Background(), tool.name, json.RawMessage(fmt.Sprintf(tool.input, dir)))
		if isErr {
			t.Fatalf("%s failed: %q", tool.name, out)
		}
		if !strings.Contains(out, "main.go") {
			t.Errorf("%s missed the file that matters: %q", tool.name, out)
		}
		for _, noise := range []string{".git", "node_modules", "vendor"} {
			if strings.Contains(out, noise) {
				t.Errorf("%s walked into %s: %q", tool.name, noise, out)
			}
		}
	}
}

// A search stops at 200 matches rather than filling the answer with them.
func TestSearchesStopAtTheMatchLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxMatches+50; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", i)), []byte("cocok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, _ := Execute(context.Background(), "glob", json.RawMessage(fmt.Sprintf(`{"pattern":"*.go","path":%q}`, dir)))
	if lines := strings.Count(out, "\n") + 1; lines > maxMatches {
		t.Errorf("glob returned %d lines, cap is %d", lines, maxMatches)
	}

	out, _ = Execute(context.Background(), "grep", json.RawMessage(fmt.Sprintf(`{"pattern":"cocok","path":%q}`, dir)))
	if lines := strings.Count(out, "\n") + 1; lines > maxMatches {
		t.Errorf("grep returned %d lines, cap is %d", lines, maxMatches)
	}
}

// One very long line — a minified bundle, say — must not swallow the whole
// result on its own.
func TestGrepShortensAVeryLongLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bundle.js"), []byte("cocok"+strings.Repeat("y", 5000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, isErr := Execute(context.Background(), "grep", json.RawMessage(fmt.Sprintf(`{"pattern":"cocok","path":%q}`, dir)))
	if isErr {
		t.Fatalf("grep failed: %q", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "…") || len(out) > 400 {
		t.Fatalf("a long line must be cut and marked: %d characters", len(out))
	}
}

// Binary files have nothing to show a human, and a search that dumps them is
// unusable.
func TestGrepSkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.bin"), []byte("cocok\x00\x01\x02"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("cocok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := Execute(context.Background(), "grep", json.RawMessage(fmt.Sprintf(`{"pattern":"cocok","path":%q}`, dir)))
	if strings.Contains(out, "app.bin") {
		t.Errorf("a binary file must be skipped: %q", out)
	}
	if !strings.Contains(out, "app.go") {
		t.Errorf("the text file must still be found: %q", out)
	}
}

// A command is killed with everything it started. Killing the shell alone
// leaves the work running with nothing left to report to.
func TestShellKillsWhatItStarted(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-alive")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Long enough for the child to be running, short enough to be quick.
		<-timeAfter(200)
		cancel()
	}()

	// The child outlives its shell unless the whole group is killed.
	_, _ = Shell(ctx, fmt.Sprintf("(sleep 1; touch %q) & wait", marker), nil)

	<-timeAfter(1500)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the child survived the kill and wrote its file")
	}
}

// The permissions page is the other half of the claim, and the same rule
// applies to it: every tool has to appear in its key table, or a capability
// exists that nobody can find the setting for.
func TestPermissionsPageListsEveryTool(t *testing.T) {
	page, err := os.ReadFile("../../docs/permissions.md")
	if err != nil {
		t.Fatalf("the permissions page must exist: %v", err)
	}

	for _, spec := range Definitions() {
		if !strings.Contains(string(page), "| `"+spec.Name+"` |") {
			t.Errorf("%s is missing from the permission keys table", spec.Name)
		}
	}
	for _, answer := range []string{"| `allow` |", "| `ask` |", "| `deny` |"} {
		if !strings.Contains(string(page), answer) {
			t.Errorf("the three answers must be documented, %s is not", answer)
		}
	}
}

// fetch_url is the one tool that leaves the machine, so most of what it does
// is refuse. The addresses below are not hypothetical: 169.254.169.254 hands
// out cloud credentials to anything that asks, and a model told to "check what
// this service returns" will try it as readily as anything else.
func TestFetchRefusesWhatIsNotThePublicInternet(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/admin",
		"http://localhost/",
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"file:///etc/passwd",
		"ftp://example.com/x",
	} {
		out, isErr := Execute(context.Background(), "fetch_url",
			json.RawMessage(`{"url":`+strconv.Quote(target)+`}`))
		if !isErr {
			t.Errorf("%s must be refused, got %q", target, out)
		}
	}
}

// The markup is most of a page and none of it is worth a token.
func TestFetchStripsMarkup(t *testing.T) {
	got := textFromHTML(`<html><head><style>body{color:red}</style>` +
		`<script>alert("x")</script></head><body><h1>Judul</h1>` +
		`<p>Dua &amp; tiga</p></body></html>`)
	if strings.Contains(got, "alert") || strings.Contains(got, "color:red") {
		t.Errorf("scripts and styles go whole: %q", got)
	}
	if !strings.Contains(got, "Judul") || !strings.Contains(got, "Dua & tiga") {
		t.Errorf("the words survive, entities included: %q", got)
	}
}
