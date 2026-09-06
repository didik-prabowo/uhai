package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tryEdit(t *testing.T, path, old, new string) (string, bool) {
	t.Helper()
	in, err := json.Marshal(map[string]string{"path": path, "old": old, "new": new})
	if err != nil {
		t.Fatal(err)
	}
	return Execute(context.Background(), "edit_file", in)
}

func TestEditFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	os.WriteFile(path, []byte("package a\n\nfunc one() {}\nfunc two() {}\n"), 0644)

	if out, isErr := tryEdit(t, path, "func one() {}", "func one() { return }"); isErr {
		t.Fatalf("a unique match should be replaced: %s", out)
	}
	got, _ := os.ReadFile(path)
	if want := "package a\n\nfunc one() { return }\nfunc two() {}\n"; string(got) != want {
		t.Fatalf("file is %q, want %q", got, want)
	}

	// An ambiguous or missing match must fail loudly and leave the file alone.
	before, _ := os.ReadFile(path)
	if _, isErr := tryEdit(t, path, "func ", "X"); !isErr {
		t.Fatal("two matches should be refused")
	}
	if _, isErr := tryEdit(t, path, "nowhere", "X"); !isErr {
		t.Fatal("a missing match should be refused")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused edit must not change the file")
	}
}

func TestWriteFileCreatesParentFolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "x.txt")
	in, _ := json.Marshal(map[string]string{"path": path, "content": "hi"})
	if out, isErr := Execute(context.Background(), "write_file", in); isErr {
		t.Fatalf("write_file should create parents: %s", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestRunBashCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in, _ := json.Marshal(map[string]string{"command": "echo hi"})
	if _, isErr := Execute(ctx, "run_bash", in); !isErr {
		t.Fatal("a cancelled context must stop the command")
	}
}

func TestGlobAndGrep(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub", ".git"), 0755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc Hello() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("package b\n\nfunc Hello() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", ".git", "c.go"), []byte("func Hello() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("Hello there\n"), 0644)

	run := func(tool string, args map[string]string) string {
		t.Helper()
		in, _ := json.Marshal(args)
		out, isErr := Execute(context.Background(), tool, in)
		if isErr {
			t.Fatalf("%s failed: %s", tool, out)
		}
		return out
	}

	// A pattern without a folder matches on the file name, at any depth, and
	// .git is never walked into.
	got := run("glob", map[string]string{"pattern": "*.go", "path": dir})
	if !strings.Contains(got, "a.go") || !strings.Contains(got, filepath.Join("sub", "b.go")) {
		t.Fatalf("glob missed a file: %s", got)
	}
	if strings.Contains(got, ".git") {
		t.Fatalf("glob walked into .git: %s", got)
	}

	// include narrows grep to Go files, so the .txt hit is left out.
	got = run("grep", map[string]string{"pattern": "func Hello", "path": dir, "include": "*.go"})
	if strings.Count(got, "\n") != 1 { // two matching lines, one newline between
		t.Fatalf("grep should have found exactly two matches: %s", got)
	}
	if !strings.Contains(got, "a.go:3:func Hello() {}") {
		t.Fatalf("grep result is not path:line:text — %s", got)
	}

	got = run("grep", map[string]string{"pattern": "Hello", "path": dir})
	if !strings.Contains(got, "notes.txt") {
		t.Fatalf("grep without include should search every file: %s", got)
	}
	if _, isErr := Execute(context.Background(), "grep", []byte(`{"pattern":"("}`)); !isErr {
		t.Fatal("a broken regular expression must be reported, not panic")
	}
}

// A command the model runs has to outlive a test suite: the old thirty seconds
// meant it could not verify its own work on anything bigger than a toy.
func TestBashTimeoutOutlivesATestSuite(t *testing.T) {
	if bashTimeout < time.Minute {
		t.Fatalf("bashTimeout is %s, too short to run tests with", bashTimeout)
	}

	// It is still a limit: a command that waits for input must die rather than
	// hold the turn open.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Shell(ctx, "sleep 30", nil); err == nil {
		t.Fatal("a command must be killed when its context ends")
	}
}

// The pattern the tool's own description offers, at the depths a real project
// has. ** in the middle used to match exactly one folder and then go quiet.
func TestMatcherSpansAnyNumberOfFolders(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "internal/tools/glob.go", true},
		{"*.go", "internal/tools/glob.md", false},
		{"**/*_test.go", "internal/cli/cli_test.go", true},
		{"**/repository/constant.go", "app/internal/repository/constant.go", true},

		// One folder deep worked before; two did not.
		{"internal/**/*.go", "internal/tools/glob.go", true},
		{"internal/**/*.go", "internal/session/filestore/filestore.go", true},
		// ** stands for no folder at all as readily as for three.
		{"internal/**/*.go", "internal/agent.go", true},
		{"internal/**/*.go", "cmd/uhai/main.go", false},

		// A single star still stops at a separator, or "*.go" would match
		// every Go file in the tree from the root.
		{"internal/*/*.go", "internal/session/filestore/filestore.go", false},
		{"?ain.go", "cmd/uhai/main.go", true},
		{"*.[ch]", "src/parse.c", true},
		{"*.[!ch]", "src/parse.c", false},
	} {
		if got := matcher(c.pattern)(c.path); got != c.want {
			t.Errorf("matcher(%q)(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A pattern that cannot compile finds nothing rather than panicking or
// matching everything.
func TestMatcherSurvivesNonsense(t *testing.T) {
	if matcher("[")("[") != true && matcher("[")("x") != false {
		t.Error("an unclosed class is a literal bracket")
	}
	if matcher("*.go")("") {
		t.Error("nothing is not a match")
	}
}

// Escape reached the HTTP call and reached bash, and stopped at the edge of
// walk: glob and grep took a context and threw it away, so a grep that finds
// nothing in a large tree read every file to the end whatever was pressed.
func TestSearchStopsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, c := range []struct{ name, input string }{
		{NameGlob, `{"pattern":"**/*.go"}`},
		{NameGrep, `{"pattern":"func "}`},
	} {
		out, isErr := Execute(ctx, c.name, json.RawMessage(c.input))
		if !isErr || !strings.Contains(out, "interrupted") {
			t.Errorf("%s ran on regardless: isErr=%v out=%q", c.name, isErr, out)
		}
	}
}

// And an uncancelled search still works, so the check is a check and not a
// wall.
func TestSearchStillWorksUninterrupted(t *testing.T) {
	out, isErr := Execute(context.Background(), NameGlob, json.RawMessage(`{"pattern":"*.go"}`))
	if isErr || !strings.Contains(out, ".go") {
		t.Errorf("a plain search must still find things: isErr=%v out=%q", isErr, out)
	}
}

// Refusing to start is the easy half. This one cancels partway through a tree
// and checks the walk actually abandons it: without the check inside the
// callback it would visit all fifty files and only notice at the end.
func TestWalkAbandonsATreePartway(t *testing.T) {
	dir := t.TempDir()
	for i := range 50 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.go", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	err := walk(ctx, dir, func(string) bool {
		seen++
		if seen == 5 {
			cancel()
		}
		return true
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("an abandoned walk has to say why, got %v", err)
	}
	// One more entry may be visited between the cancel and the next check.
	if seen > 6 {
		t.Errorf("the walk read %d of 50 files after being cancelled at 5", seen)
	}
}

// maxMatches bounds a search that finds too much. Nothing bounded one that
// finds too little in a tree too large, and grep reads the contents of every
// file it walks — so a turn was hostage to it, with nothing to do but wait or
// throw the whole turn away.
func TestSearchGivesUpRatherThanHoldingTheTurn(t *testing.T) {
	was := searchTimeout
	searchTimeout = time.Nanosecond
	t.Cleanup(func() { searchTimeout = was })

	for _, c := range []struct{ name, input string }{
		{NameGlob, `{"pattern":"**/*.go"}`},
		{NameGrep, `{"pattern":"func "}`},
	} {
		out, isErr := Execute(context.Background(), c.name, json.RawMessage(c.input))
		if !isErr {
			t.Errorf("%s: a search that found nothing in time is an error, got %q", c.name, out)
		}
		if !strings.Contains(out, "narrow it") {
			t.Errorf("%s: the model has to be told what to do next, got %q", c.name, out)
		}
		if strings.Contains(out, "interrupted") {
			t.Errorf("%s: running out of time is not the user pressing a key: %q", c.name, out)
		}
	}
}

// Whatever was found before the budget ran out comes back: half a large tree
// is usually already past where the answer was, and throwing it away means
// paying for the walk twice.
func TestATimedOutSearchKeepsWhatItFound(t *testing.T) {
	if note, isErr := searchNote(context.DeadlineExceeded, 12); isErr || !strings.Contains(note, "partial") {
		t.Errorf("found results survive the deadline: isErr=%v note=%q", isErr, note)
	}
	if note, isErr := searchNote(context.Canceled, 12); !isErr || !strings.Contains(note, "interrupted") {
		t.Errorf("a keypress is still a keypress: isErr=%v note=%q", isErr, note)
	}
	if note, _ := searchNote(nil, 12); note != "" {
		t.Errorf("a search that finished has nothing to explain, got %q", note)
	}
}

// Asked for donut recipes, a model invented seven cookpad ids in a row — two
// of them the same number with different titles — because fetch_url is the
// only door to the web and nothing told it the door needs an address it
// already has. A 404 is where it finds out, so the 404 says what to do.
func TestA404TellsTheModelNotToGuessAgain(t *testing.T) {
	got := statusMessage(404, "https://cookpad.com/id/recipe/13356818-donat-kentang")
	for _, want := range []string{"does not exist", "Do not guess another", "no search tool"} {
		if !strings.Contains(got, want) {
			t.Errorf("the 404 has to say %q, got %q", want, got)
		}
	}

	// Only a 404 gets the lecture: a 500 is the site's problem, and trying
	// again is a reasonable thing to do about it.
	if other := statusMessage(500, "https://example.com"); strings.Contains(other, "Do not guess") {
		t.Errorf("a 500 is not a wrong address, got %q", other)
	}
	if !strings.Contains(statusMessage(500, "https://example.com"), "HTTP 500") {
		t.Error("every status still says what it was")
	}
}

// The tool description is the only place the model learns there is no search,
// and it learns it before spending four minutes finding out.
func TestFetchSaysItCannotSearch(t *testing.T) {
	var fetch Tool
	for _, tl := range all {
		if tl.Name() == NameFetch {
			fetch = tl
		}
	}
	if fetch == nil {
		t.Fatal("fetch_url is not registered")
	}
	for _, want := range []string{"not a search engine", "Never invent or guess a URL"} {
		if !strings.Contains(fetch.Description(), want) {
			t.Errorf("the description has to say %q", want)
		}
	}
}
