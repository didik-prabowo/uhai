package orchestrator

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
)

// A conversation is resumed with the model it was held with. Carrying on with
// whatever settings.json says today would change the context window and the
// price of every turn without saying so.
func TestResumeBringsBackTheModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")

	saved := session.Session{
		Started: time.Now(),
		Model:   "openai/gpt-4o-mini",
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "where were we"}},
		}},
	}
	// -resume with no id takes this project's newest, so the fixture has to
	// say which project it is — the point of the field.
	saved.Root, _ = os.Getwd()
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}

	a := agent.New(nil)
	note, err := restore(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("nothing should have been lost: %s", note)
	}
	if len(a.History) != 1 {
		t.Fatalf("history did not come back: %+v", a.History)
	}
	if a.Provider == nil || a.Provider.Name() != saved.Model {
		t.Fatalf("resumed on the wrong model: %v", a.Provider)
	}
	if a.MaxContextTokens == 0 {
		t.Error("the window follows the model, so it has to be set with it")
	}

	// A model that can no longer be built is a line, not a silent swap.
	saved.Model = "nosuchprovider/x"
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	b := agent.New(nil)
	note, err = restore(b, "")
	if err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Error("failing to restore the model must be said out loud")
	}
}

// An id picks which conversation comes back, not just the newest.
func TestResumeByID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	msg := provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "older"}}}
	older, newer := session.New(), session.New()
	older.Started, older.Model, older.Messages = time.Now().Add(-2*time.Hour), "openai/older", []provider.Message{msg}
	newer.Started, newer.Model, newer.Messages = time.Now(), "openai/newer", []provider.Message{msg}
	here, _ := os.Getwd()
	older.Root, newer.Root = here, here
	for _, s := range []session.Session{older, newer} {
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	a := agent.New(nil)
	if _, err := restore(a, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := restore(a, "nosuchid"); err == nil {
		t.Error("an id nobody saved must be an error, not an empty start")
	}
}

// newAgent is the composition root, and every promise it keeps is invisible
// from inside any one package: the notes file that ends up in the system
// prompt, the skills that only travel as names, the deny list the agent takes
// as a function so it never has to read settings itself. A rename or a moved
// default breaks these silently — the turn simply goes out without them.
func TestNewAgentComposesTheSession(t *testing.T) {
	dir := workIn(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("UHAI_MODEL", "openai/gpt-4o-mini")

	write(t, filepath.Join(dir, "UHAI.md"), "Jalankan test dengan CGO_ENABLED=0.")
	write(t, filepath.Join(dir, ".uhai", "skills", "planning", "SKILL.md"),
		"---\nname: planning\ndescription: Rencanakan dulu\n---\nBadan panjang yang tidak ikut ke prompt.\n")
	write(t, filepath.Join(dir, ".uhai", "settings.json"), `{"permissions":{"deny":["run_bash"]}}`)

	a, err := newAgent()
	if err != nil {
		t.Fatal(err)
	}
	if a.Provider == nil || a.MaxContextTokens == 0 || !a.UseTools {
		t.Fatalf("the model's own facts must follow it in: %v %d %v", a.Provider, a.MaxContextTokens, a.UseTools)
	}

	// The notes file is named in the header the model reads, so the header is
	// worth asserting too: it is what turns pasted text into instructions.
	if !strings.Contains(a.System, "# Project instructions") || !strings.Contains(a.System, "CGO_ENABLED=0") {
		t.Error("UHAI.md must reach the system prompt, under a header saying what it is")
	}
	if !strings.Contains(a.System, "UHAI.md") {
		t.Error("the header must name the file it came from")
	}

	// Only the name and description travel; the body is a path to open later.
	if !strings.Contains(a.System, "planning") || !strings.Contains(a.System, "Rencanakan dulu") {
		t.Error("a skill must be announced by name and description")
	}
	if strings.Contains(a.System, "Badan panjang") {
		t.Error("a skill's body must not be paid for on every turn")
	}

	if a.AllowTool("run_bash", "") {
		t.Error("a denied tool must never be offered to the model")
	}
	if !a.AllowTool("read_file", "") {
		t.Error("denying one tool must not deny the rest")
	}
}

// The one-shot run is what scripts and pipes get. The split matters as much as
// the answer: the answer goes to stdout so it can be piped, and everything
// else to stderr so it does not land in the pipe with it.
func TestRunOnceAnswersOnStdout(t *testing.T) {
	workIn(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("UHAI_MODEL", "openai/gpt-4o-mini")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"halo\"}}],\"usage\":{\"prompt_tokens\":1200,\"completion_tokens\":7}}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()
	t.Setenv("UHAI_BASE_URL", srv.URL)

	var runErr error
	out := captureStdout(t, func() { runErr = RunOnce("hi", false) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "halo") {
		t.Errorf("the answer must reach stdout, got %q", out)
	}

	// -p used to be the one door that saved nothing: it answers from here
	// rather than through cli, so it never met saveSession. An answer nobody
	// can resume or even find in -sessions is an answer that did not happen.
	saved, err := store.All()
	if err != nil {
		t.Fatalf("a one-shot answer must be saved like any other: %v", err)
	}
	if len(saved) != 1 || saved[0].Prompt() != "hi" {
		t.Fatalf("the prompt asked must be what was saved: %+v", saved)
	}
	if len(saved[0].Messages) != 2 {
		t.Errorf("both sides of the turn belong in the file, got %d", len(saved[0].Messages))
	}

	// And it was the one door that recorded nothing either: RunOnce set no
	// OnUsage, so a -p turn was paid for and written down nowhere, and a
	// -resume picked it up believing it had cost nothing so far.
	if len(saved[0].Spend) != 1 {
		t.Fatalf("a one-shot turn has to record what it cost: %+v", saved[0].Spend)
	}
	if got := saved[0].Spend[0]; got.Model != "openai/gpt-4o-mini" || got.Usage.Input != 1200 || got.Usage.Output != 7 {
		t.Errorf("the tokens must be stored against the model that burned them, got %+v", got)
	}
}

// No provider is fatal here and nowhere else: the interactive session opens
// without one so /connect can fix it from inside, but a pipe has nobody to ask.
func TestRunOnceWithoutAProviderIsFatal(t *testing.T) {
	workIn(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("UHAI_MODEL", "openai/gpt-4o-mini")

	if err := RunOnce("hi", false); err == nil {
		t.Error("a one-shot run with no key must fail rather than answer nothing")
	}
}

// The listing is a command, not a screen, so its one job is to be readable
// enough to copy an id out of: newest first, and each line carrying the prompt
// that started it. A listing where every row looks the same answers nothing.
func TestListSessionsIsReadableEnoughToCopyFrom(t *testing.T) {
	workIn(t)

	msg := func(text string) []provider.Message {
		return []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}}
	}
	older, newer := session.New(), session.New()
	older.Started, older.Model, older.Messages = time.Now().Add(-2*time.Hour), "openai/gpt-4o-mini", msg("yang lama")
	newer.Started, newer.Model, newer.Messages = time.Now(), "zai/glm-4.6", msg("yang baru")
	// The listing shows this project's conversations, so the fixtures have to
	// be in it: without a root they are counted as older-and-unplaceable, and
	// that count is the only thing printed.
	here, _ := os.Getwd()
	older.Root, newer.Root = here, here
	for _, s := range []session.Session{older, newer} {
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	var listErr error
	out := captureStdout(t, func() { listErr = ListSessions() })
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, want := range []string{"yang lama", "yang baru", "zai/glm-4.6", newer.ID, fmt.Sprintf("pid %d", os.Getpid())} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing must carry %q, got:\n%s", want, out)
		}
	}
	if strings.Index(out, "yang baru") > strings.Index(out, "yang lama") {
		t.Errorf("newest first, got:\n%s", out)
	}
}

// workIn gives the test a home and a working directory of its own, so the
// settings, credentials and project notes it finds are only the ones it wrote.
func workIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureStdout swaps os.Stdout for a pipe. fmt.Println looks the variable up
// at call time, so this reaches the prints inside RunOnce without RunOnce
// having to take a writer it would otherwise never need.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		io.Copy(&b, r)
		done <- b.String()
	}()

	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// The listing is this project's, and says where the rest went rather than
// letting a hundred conversations vanish without explanation.
func TestListSessionsKeepsToThisProjectAndSaysSo(t *testing.T) {
	workIn(t)
	here, _ := os.Getwd()

	msg := func(text string) []provider.Message {
		return []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}}
	}
	mine, theirs, old := session.New(), session.New(), session.New()
	mine.Root, mine.Model, mine.Messages = here, "zai/punya-sini", msg("di sini")
	theirs.Root, theirs.Model, theirs.Messages = filepath.Join(here, "..", "lain"), "zai/punya-sana", msg("di sana")
	old.Model, old.Messages = "zai/lama", msg("sebelum root ada")
	for _, s := range []session.Session{mine, theirs, old} {
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	out := captureStdout(t, func() {
		if err := ListSessions(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "punya-sini") {
		t.Errorf("this project's conversation is missing:\n%s", out)
	}
	if strings.Contains(out, "punya-sana") {
		t.Errorf("another project's conversation was listed:\n%s", out)
	}
	// Neither is silently dropped.
	if !strings.Contains(out, "1 older conversation") {
		t.Errorf("the unplaceable one has to be accounted for:\n%s", out)
	}
	if !strings.Contains(out, "1 conversation(s) belong to other projects") {
		t.Errorf("the other project's has to be accounted for:\n%s", out)
	}
}

// lastLine is what tells a worker's result from whatever the model printed on
// its way there, so it has to survive both.
func TestLastLineIsTheResult(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"{\"report\":\"a\"}", `{"report":"a"}`},
		{"noise\n{\"report\":\"a\"}\n", `{"report":"a"}`},
		{"{\"report\":\"a\"}\n\n\n", `{"report":"a"}`},
		{"", ""},
		{"   \n  \n", ""},
	} {
		if got := lastLine([]byte(c.in)); got != c.want {
			t.Errorf("lastLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A rule naming a path used to match nothing on a tool that never asks.
// `config.Permission` was reached only through `Confirm`, and `Confirm` is asked
// only for tools whose effects escape the process — so `"deny": ["Read(./.env)"]`,
// the example in config's own package comment, was inert. This is the wiring
// that makes it mean something, tested where it is wired.
func TestAPathRuleReachesTheToolsThatNeverAsk(t *testing.T) {
	dir := workIn(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("UHAI_MODEL", "openai/gpt-4o-mini")
	write(t, filepath.Join(dir, ".uhai", "settings.json"),
		`{"permissions":{"deny":["Read(./secret.txt)"]}}`)

	a, err := newAgent()
	if err != nil {
		t.Fatal(err)
	}

	if a.AllowTool("read_file", `{"path":"secret.txt"}`) {
		t.Error("a denied path must be refused, not read")
	}
	if !a.AllowTool("read_file", `{"path":"main.go"}`) {
		t.Error("denying one path must not deny the rest")
	}
	// Reading is the tool with no confirmation in front of it, so the rule has
	// to be answered here. Writing has one, and is left to it.
	if !a.AllowTool("write_file", `{"path":"secret.txt"}`) {
		t.Error("a tool that confirms must be left to its confirmation")
	}
}

// The default half of the same thing: a file that is a credential by convention
// is refused with no settings at all, because until now `read_file .env` put one
// in the history, the request and the session file without anybody being asked.
func TestCredentialFilesAreRefusedByDefault(t *testing.T) {
	workIn(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("UHAI_MODEL", "openai/gpt-4o-mini")

	a, err := newAgent()
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{".env", "config/.env.production", "deploy.pem", "/home/x/.ssh/id_rsa"} {
		if a.AllowTool("read_file", `{"path":"`+path+`"}`) {
			t.Errorf("%s is a credential by convention and was read without a word", path)
		}
	}
	// And the ones that look like it and are checked in on purpose.
	for _, path := range []string{".env.example", "main.go", "docs/guide/tools.md"} {
		if !a.AllowTool("read_file", `{"path":"`+path+`"}`) {
			t.Errorf("%s is an ordinary file and must stay readable", path)
		}
	}
}
