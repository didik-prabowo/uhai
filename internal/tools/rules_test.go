package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// timeAfter keeps the waits in this file readable in milliseconds.
func timeAfter(ms int) <-chan time.Time { return time.After(time.Duration(ms) * time.Millisecond) }

// docsPath is the page these tests hold to the code. A tool the model can call
// and nobody documented is how a surprise gets shipped.
const docsPath = "../../docs/guide/tools.md"

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
			t.Errorf("%s is offered to the model but has no section in docs/guide/tools.md", spec.Name)
		}
	}

	// The sections named after tools are the ones whose heading looks like an
	// identifier; the prose ones ("Configure", "Internals") do not.
	for _, m := range regexp.MustCompile(`(?m)^### ([a-z_]+)$`).FindAllStringSubmatch(page, -1) {
		if !defined[m[1]] {
			t.Errorf("docs/guide/tools.md documents %q, which no longer exists", m[1])
		}
	}
}

// The page opens by saying how many tools there are, and that sentence went
// stale the first time one was added: it said seven while the code offered
// eight. A number in prose is a claim like any other on this page.
func TestTheToolCountOnThePageIsRight(t *testing.T) {
	words := []string{"zero", "one", "two", "three", "four", "five", "six",
		"seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen"}

	n := len(Definitions())
	if n >= len(words) {
		t.Skipf("no word for %d tools; spell it in the table above", n)
	}
	if want := "uhai has " + words[n] + ","; !strings.Contains(docs(t), want) {
		t.Errorf("the page has to say %q", want)
	}

	// The README says it too, and had drifted further: "Six tools" while the
	// code offered eleven. Only the count is held here — the sentence after
	// it numbers spawn_task as the one past them, and whoever comes to fix
	// this line will be looking straight at it.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("the README must exist: %v", err)
	}
	want := strings.ToUpper(words[n][:1]) + words[n][1:] + " tools:"
	if !strings.Contains(string(readme), want) {
		t.Errorf("the README has to say %q", want)
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
		"read_file":   false,
		"glob":        false,
		"grep":        false,
		"find_symbol": false,
		"write_file":  true,
		"edit_file":   true,
		"run_bash":    true,
		"fetch_url":   true,
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
		"read_file":      {"path"},
		"write_file":     {"path", "content"},
		"edit_file":      {"path", "old", "new"},
		"glob":           {"pattern"},
		"fetch_url":      {"url"},
		"grep":           {"pattern"},
		"find_symbol":    {"name"},
		"run_bash":       {"command"},
		"set_plan":       {"steps"},
		"search_web":     {"query"},
		"list_directory": nil,
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
	out, isErr := Execute(context.Background(), "", "delete_everything", json.RawMessage(`{}`))
	if !isErr || !strings.Contains(out, "unknown tool") {
		t.Fatalf("got %q, isError=%v", out, isErr)
	}
}

// A result is cut at the end, so what a command printed last — where a failure
// explains itself — is what survives.
func TestLongResultsAreTruncated(t *testing.T) {
	// grep, and no longer read_file: a read now stops at its own budget and
	// says which lines it gave, so it never reaches this cut. grep can — two
	// hundred matches of two hundred characters is forty thousand — and the
	// cut is still the backstop for every tool that has no budget of its own.
	dir := t.TempDir()
	long := strings.Repeat("needle ", 30) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(strings.Repeat(long, 300)), 0o644); err != nil {
		t.Fatal(err)
	}

	out, isErr := Execute(context.Background(), "", NameGrep,
		json.RawMessage(fmt.Sprintf(`{"pattern":"needle","path":%q}`, dir)))
	if isErr {
		t.Fatalf("a search with many hits is not an error: %q", out)
	}
	if !strings.HasSuffix(out, "...[output truncated]") {
		t.Fatal("a truncated result must say so, or the model treats half of it as the whole")
	}
	if len(out) > maxResultLen+len("\n...[output truncated]") {
		t.Fatalf("result is %d characters, cap is %d", len(out), maxResultLen)
	}
}

// A file larger than one result is the ordinary case, not the exception:
// internal/cli/cli_test.go is 107,890 bytes against an 8,000-character cap, so
// the model could reach the same 7% of it however many times it asked. A window
// plus the offset to carry on is the whole fix; an empty result and a truncated
// one both read as "this is the file".
func TestReadFileGivesAWindowAndSaysHowToCarryOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.go")
	var body strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	read := func(args string) string {
		t.Helper()
		out, isErr := Execute(context.Background(), "", NameRead, json.RawMessage(args))
		if isErr {
			t.Fatalf("%s: %s", args, out)
		}
		return out
	}

	// The first window starts at the start and names where to continue.
	first := read(fmt.Sprintf(`{"path":%q}`, path))
	if !strings.Contains(first, "line 1\n") {
		t.Error("a read with no offset starts at the beginning")
	}
	if !strings.Contains(first, "more follow") || !strings.Contains(first, "offset") {
		t.Errorf("it must say how to reach the rest:\n%s", first[max(0, len(first)-120):])
	}

	// And the offset it named actually continues from there, rather than
	// handing back the same page again.
	next := regexp.MustCompile(`offset (\d+)`).FindStringSubmatch(first)
	if next == nil {
		t.Fatal("no offset in the note")
	}
	second := read(fmt.Sprintf(`{"path":%q,"offset":%s}`, path, next[1]))
	if strings.Contains(second, "line 1\n") {
		t.Error("the second window is the same as the first")
	}
	if !strings.Contains(second, "line "+next[1]+"\n") {
		t.Errorf("the window must start at the line it was asked for, not near it")
	}

	// limit is honoured, and a window that is not the start says where it sat —
	// the text alone does not say which lines these are.
	window := read(fmt.Sprintf(`{"path":%q,"offset":4990,"limit":3}`, path))
	if !strings.Contains(window, "line 4990") || !strings.Contains(window, "line 4992") ||
		strings.Contains(window, "line 4993") {
		t.Errorf("limit must be exact:\n%s", window)
	}

	// Past the end is an error naming the length, since the useful next move is
	// a smaller offset and a refusal alone does not suggest one.
	out, isErr := Execute(context.Background(), "", NameRead,
		json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":99999}`, path)))
	if !isErr || !strings.Contains(out, "5000 lines") {
		t.Errorf("an offset past the end must say how long the file is: %q", out)
	}

	// One line longer than the whole budget must not come back empty: an empty
	// result leaves the same offset as the only thing to try, which is a loop.
	huge := filepath.Join(dir, "oneline.txt")
	if err := os.WriteFile(huge, []byte(strings.Repeat("x", maxResultLen*2)), 0o644); err != nil {
		t.Fatal(err)
	}
	cut := read(fmt.Sprintf(`{"path":%q}`, huge))
	if !strings.Contains(cut, "line cut") || len(cut) < 1000 {
		t.Errorf("a single enormous line must come back cut, not missing: %q", truncate(cut))
	}
}

// An empty file is a fact worth stating: an empty result reads as a tool that
// failed quietly.
func TestReadingAnEmptyFileSaysSo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, isErr := Execute(context.Background(), "", NameRead, json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
	if isErr || !strings.Contains(out, "empty") {
		t.Errorf("an empty file must say it is empty, got %q", out)
	}
}

// Reading what is not there is an error the model can act on, not an empty
// string it would take for an empty file.
func TestReadingWhatIsNotThereIsAnError(t *testing.T) {
	out, isErr := Execute(context.Background(), "", "read_file", json.RawMessage(`{"path":"/nowhere/at/all.go"}`))
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
		out, isErr := Execute(context.Background(), "", "edit_file", json.RawMessage(fmt.Sprintf(c.input, path)))
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
		out, isErr := Execute(context.Background(), "", tool.name, json.RawMessage(fmt.Sprintf(tool.input, dir)))
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

	out, _ := Execute(context.Background(), "", "glob", json.RawMessage(fmt.Sprintf(`{"pattern":"*.go","path":%q}`, dir)))
	if lines := strings.Count(out, "\n") + 1; lines > maxMatches {
		t.Errorf("glob returned %d lines, cap is %d", lines, maxMatches)
	}

	out, _ = Execute(context.Background(), "", "grep", json.RawMessage(fmt.Sprintf(`{"pattern":"cocok","path":%q}`, dir)))
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

	out, isErr := Execute(context.Background(), "", "grep", json.RawMessage(fmt.Sprintf(`{"pattern":"cocok","path":%q}`, dir)))
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

	out, _ := Execute(context.Background(), "", "grep", json.RawMessage(fmt.Sprintf(`{"pattern":"cocok","path":%q}`, dir)))
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
	_, _ = Shell(ctx, "", fmt.Sprintf("(sleep 1; touch %q) & wait", marker), nil)

	<-timeAfter(1500)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the child survived the kill and wrote its file")
	}
}

// The permissions page is the other half of the claim, and the same rule
// applies to it: every tool has to appear in its key table, or a capability
// exists that nobody can find the setting for.
func TestPermissionsPageListsEveryTool(t *testing.T) {
	page, err := os.ReadFile("../../docs/guide/permissions.md")
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
		out, isErr := Execute(context.Background(), "", "fetch_url",
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

// The name in Definitions and the name in Execute's switch are two strings
// that only happen to match. Rename one — "run_bash" to "bash", say — and the
// build passes, every other test passes, and the model gets "unknown tool" the
// first time it tries. Nothing else notices, because nothing else can.
//
// The empty object is deliberate: every tool has to reject it, so this reaches
// the dispatch without any of them doing their job. A tool that ran on {} and
// did something would be a finding of its own.
func TestEveryToolIsActuallyDispatched(t *testing.T) {
	for _, spec := range Definitions() {
		out, _ := Execute(context.Background(), "", spec.Name, json.RawMessage(`{}`))
		if strings.Contains(out, "unknown tool") {
			t.Errorf("%s is offered to the model but Execute has no case for it", spec.Name)
		}
	}
	// And the other direction, so the check cannot pass by accident.
	if out, _ := Execute(context.Background(), "", "nope", json.RawMessage(`{}`)); !strings.Contains(out, "unknown tool") {
		t.Errorf("a tool nobody defined must be refused, got %q", out)
	}
}

// docs/guide/permissions.md prints the credential list, and a page that
// disagrees with the code is worse than no page: somebody reads it, writes no
// rule, and expects a file to be refused that is not. So the two are one list
// with two spellings, and this fails if they part.
func TestDocumentedCredentialListMatchesTheCode(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "guide", "permissions.md"))
	if err != nil {
		t.Fatal(err)
	}

	block := regexp.MustCompile("(?s)### Credentials are the exception.*?```\n(.*?)```")
	found := block.FindSubmatch(page)
	if found == nil {
		t.Fatal("the page no longer prints the credential list")
	}

	documented := map[string]bool{}
	for _, word := range strings.Fields(string(found[1])) {
		documented[strings.TrimSuffix(word, "/")] = true
	}

	for _, name := range append(append([]string{}, secretNames...), secretDirs...) {
		if !documented[name] {
			t.Errorf("%s is refused by the code and not on the page", name)
		}
	}
	for word := range documented {
		if !slices.Contains(secretNames, word) && !slices.Contains(secretDirs, word) {
			t.Errorf("the page promises %s is refused and the code does not refuse it", word)
		}
	}
	for _, open := range openSecrets {
		if !strings.Contains(string(page), "`"+open+"`") {
			t.Errorf("%s is allowed by the code and the page does not say so", open)
		}
	}
}

// The list itself, and the two ways it is deliberately narrow: a template is not
// a secret, and a name is never a guess.
func TestCredentialsByConvention(t *testing.T) {
	for _, path := range []string{
		".env", ".env.production", "app/.env.local", "deploy.pem", "server.key",
		"id_rsa", "id_ed25519.pub", ".netrc", "/home/x/.ssh/known_hosts",
		"/home/x/.aws/credentials", "/Users/x/.uhai/auth.json",
	} {
		if !SecretPath(path) {
			t.Errorf("%s holds a credential by convention and was not recognised", path)
		}
	}
	for _, path := range []string{
		".env.example", ".env.sample", "main.go", "README.md", "keyboard.go",
		"internal/config/permission.go", "",
	} {
		if SecretPath(path) {
			t.Errorf("%s is an ordinary file and must not be refused", path)
		}
	}
}

// Every tool's output goes through Execute, so redaction goes there once rather
// than into each tool. What it catches is a shape a vendor reserves — not
// anything that merely looks random, which is every git sha in a repository.
func TestCredentialShapesAreTakenOutOfEveryResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	body := `{
  "anthropic": "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
  "github":    "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789xy",
  "aws":       "AKIAIOSFODNN7EXAMPLE",
  "commit":    "7148d6c4872a61b1402f262fd929e36eb4a1c2d3",
  "note":      "keep this line"
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	in, _ := json.Marshal(map[string]string{"path": path})
	out, isErr := Execute(context.Background(), "", NameRead, in)
	if isErr {
		t.Fatalf("reading it is not an error: %s", out)
	}

	for _, secret := range []string{"sk-ant-api03-AbCd", "ghp_AbCd", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s reached the result, and from there the provider", secret)
		}
	}
	for _, kind := range []string{"anthropic api key", "github token", "aws access key id"} {
		if !strings.Contains(out, redactedPrefix+kind+"]") {
			t.Errorf("the marker must name what was taken out, missing %q:\n%s", kind, out)
		}
	}
	// A sha is forty hex characters of high entropy and is not a secret. This is
	// the false positive the whole prefix-anchored design exists to avoid.
	if !strings.Contains(out, "7148d6c4872a61b1402f262fd929e36eb4a1c2d3") {
		t.Error("a commit sha was redacted, which is the mistake entropy detection makes")
	}
	if !strings.Contains(out, "keep this line") {
		t.Error("ordinary text must survive")
	}

	// And a result with nothing in it comes back untouched, which is the path
	// every other read takes.
	plain := "package main\n\nfunc main() {}\n"
	if got := Redact(plain); got != plain {
		t.Errorf("an ordinary file must not be rewritten: %q", got)
	}

	// The word boundaries, which are load-bearing and were added because these
	// three failed: `sk-` matched inside `task-` and `MSG.` inside SendGrid's
	// shape, so a CI URL came back as an OpenAI key.
	for _, ordinary := range []string{
		"see https://ci.example.com/task-a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8",
		"disk-usage-report-0123456789abcdefghijklmnopqrstuv",
		"MSG.0123456789abcdefghij.0123456789abcdefghij",
	} {
		if got := Redact(ordinary); got != ordinary {
			t.Errorf("false positive:\n  in:  %s\n  out: %s", ordinary, got)
		}
	}
}

// A marker is not the file's text, so using it as the text to replace cannot
// work — and "read it again" would send the model round the same loop, since
// reading it again returns the same marker.
func TestEditSaysWhyARedactedValueCannotBeReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".config")
	if err := os.WriteFile(path, []byte("key = sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	in, _ := json.Marshal(map[string]string{
		"path": path,
		"old":  "key = " + redactedPrefix + "anthropic api key]",
		"new":  "key = something else",
	})
	out, isErr := Execute(context.Background(), "", NameEdit, in)
	if !isErr {
		t.Fatal("it cannot have replaced a marker that is not in the file")
	}
	if !strings.Contains(out, "redacted") {
		t.Errorf("the reason must name the redaction, not a stale read: %q", out)
	}
}

// grep reads every file it walks, so a credential with no shape — a password in
// a .env — would come back as a matching line. The same list read_file is
// refused by is skipped here, and the count is said rather than left silent.
func TestGrepSkipsCredentialFilesAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_PASSWORD=hunter2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("// DB_PASSWORD comes from the environment\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	in, _ := json.Marshal(map[string]string{"pattern": "DB_PASSWORD", "path": dir})
	out, isErr := Execute(context.Background(), "", NameGrep, in)
	if isErr {
		t.Fatalf("grep failed: %s", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Error("a password with no shape came back as a matching line")
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("the ordinary file must still match: %s", out)
	}
	if !strings.Contains(out, "skipped") {
		t.Errorf("a search that left files out must say so: %s", out)
	}
}
