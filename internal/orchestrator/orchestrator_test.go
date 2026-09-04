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
	t.Setenv("GROQ_API_KEY", "k")

	saved := session.Session{
		Started: time.Now(),
		Model:   "groq/openai/gpt-oss-120b",
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "where were we"}},
		}},
	}
	if err := saved.Save(); err != nil {
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
	if err := saved.Save(); err != nil {
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
	older := session.Session{Started: time.Now().Add(-2 * time.Hour), Messages: []provider.Message{msg}}
	newer := session.Session{Started: time.Now(), Messages: []provider.Message{msg}}
	for _, s := range []session.Session{older, newer} {
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}

	a := agent.New(nil)
	if _, err := restore(a, older.Started.Format("2006-01-02T15-04-05")); err != nil {
		t.Fatal(err)
	}
	if _, err := restore(a, "2029-01-01T00-00-00"); err == nil {
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
	t.Setenv("GROQ_API_KEY", "k")
	t.Setenv("UHAI_MODEL", "groq/llama-3.3-70b-versatile")

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

	if a.AllowTool("run_bash") {
		t.Error("a denied tool must never be offered to the model")
	}
	if !a.AllowTool("read_file") {
		t.Error("denying one tool must not deny the rest")
	}
}

// The one-shot run is what scripts and pipes get. The split matters as much as
// the answer: the answer goes to stdout so it can be piped, and everything
// else to stderr so it does not land in the pipe with it.
func TestRunOnceAnswersOnStdout(t *testing.T) {
	workIn(t)
	t.Setenv("GROQ_API_KEY", "k")
	t.Setenv("UHAI_MODEL", "groq/llama-3.3-70b-versatile")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"halo\"}}]}\n\ndata: [DONE]\n")
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
	saved, err := session.All()
	if err != nil {
		t.Fatalf("a one-shot answer must be saved like any other: %v", err)
	}
	if len(saved) != 1 || saved[0].Prompt() != "hi" {
		t.Fatalf("the prompt asked must be what was saved: %+v", saved)
	}
	if len(saved[0].Messages) != 2 {
		t.Errorf("both sides of the turn belong in the file, got %d", len(saved[0].Messages))
	}
}

// No provider is fatal here and nowhere else: the interactive session opens
// without one so /connect can fix it from inside, but a pipe has nobody to ask.
func TestRunOnceWithoutAProviderIsFatal(t *testing.T) {
	workIn(t)
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("UHAI_MODEL", "groq/llama-3.3-70b-versatile")

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
	older := session.Session{Started: time.Now().Add(-2 * time.Hour), Model: "groq/llama-3.3-70b-versatile", Messages: msg("yang lama")}
	newer := session.Session{Started: time.Now(), Model: "zai/glm-4.6", Messages: msg("yang baru")}
	for _, s := range []session.Session{older, newer} {
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}

	var listErr error
	out := captureStdout(t, func() { listErr = ListSessions() })
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, want := range []string{"yang lama", "yang baru", "zai/glm-4.6", newer.Started.Format("2006-01-02T15-04-05")} {
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
