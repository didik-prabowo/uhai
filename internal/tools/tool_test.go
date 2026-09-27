package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	return Execute(context.Background(), "", "edit_file", in)
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
	if out, isErr := Execute(context.Background(), "", "write_file", in); isErr {
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
	if _, isErr := Execute(ctx, "", "run_bash", in); !isErr {
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
		out, isErr := Execute(context.Background(), "", tool, in)
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
	if _, isErr := Execute(context.Background(), "", "grep", []byte(`{"pattern":"("}`)); !isErr {
		t.Fatal("a broken regular expression must be reported, not panic")
	}
}

// A command the model runs has to outlive a test suite: the old thirty seconds
// meant it could not verify its own work on anything bigger than a toy.
func TestBashTimeoutOutlivesATestSuite(t *testing.T) {
	if BashTimeout < time.Minute {
		t.Fatalf("BashTimeout is %s, too short to run tests with", BashTimeout)
	}

	// It is still a limit: a command that waits for input must die rather than
	// hold the turn open.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Shell(ctx, "", "sleep 30", nil); err == nil {
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
		out, isErr := Execute(ctx, "", c.name, json.RawMessage(c.input))
		if !isErr || !strings.Contains(out, "interrupted") {
			t.Errorf("%s ran on regardless: isErr=%v out=%q", c.name, isErr, out)
		}
	}
}

// And an uncancelled search still works, so the check is a check and not a
// wall.
func TestSearchStillWorksUninterrupted(t *testing.T) {
	out, isErr := Execute(context.Background(), "", NameGlob, json.RawMessage(`{"pattern":"*.go"}`))
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
	err := walk(ctx, dir, dir, func(string) bool {
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
		out, isErr := Execute(context.Background(), "", c.name, json.RawMessage(c.input))
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
	for _, want := range []string{"does not exist", "Do not guess another", "search_web"} {
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

// fetch_url opens an address and does not find one. Now that search_web
// exists, the description has to send the model there rather than telling it
// there is nowhere to go — the old sentence said "there is no search tool
// here", which would have hidden the tool sitting next to it.
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
	for _, want := range []string{"not a search engine", "search_web", "Never invent or guess a URL"} {
		if !strings.Contains(fetch.Description(), want) {
			t.Errorf("the description has to say %q", want)
		}
	}
}

// A search used to walk into dist/, target/ and .venv/ — slow, and answering
// with files nobody wrote. The project already says what not to look at.
func TestTheProjectsOwnIgnoresAreObeyed(t *testing.T) {
	dir := t.TempDir()
	write := func(path, body string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "# a comment\n\n/uhai\ndist/\n*.log\n!penting.log\n")
	write("main.go", "package main")
	write("uhai", "binary")              // anchored at the root
	write("dist/bundle.js", "generated") // a whole directory
	write("internal/debug.log", "noise") // by extension, at any depth
	write("internal/keep.go", "package x")
	// The negation is not honoured, and the comment above readIgnore says so:
	// a rule read but half-obeyed is worse than one never read.
	write("penting.log", "wanted")

	var seen []string
	if err := walk(context.Background(), dir, dir, func(path string) bool {
		seen = append(seen, strings.TrimPrefix(path, dir+"/"))
		return true
	}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(seen, " ")

	for _, want := range []string{"main.go", "internal/keep.go", ".gitignore"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s should still be walked, got %v", want, seen)
		}
	}
	for _, gone := range []string{"uhai", "dist/bundle.js", "internal/debug.log"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s is ignored by the project and was walked anyway: %v", gone, seen)
		}
	}
}

// An anchored rule means the root and nowhere else: /uhai is the built binary,
// not every file called uhai in the tree.
func TestAnAnchoredIgnoreStaysAtTheRoot(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/uhai\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "uhai"), []byte("binary"), 0o644)
	os.MkdirAll(filepath.Join(dir, "cmd", "uhai"), 0o755)
	os.WriteFile(filepath.Join(dir, "cmd", "uhai", "main.go"), []byte("package main"), 0o644)

	var seen []string
	walk(context.Background(), dir, dir, func(path string) bool {
		seen = append(seen, strings.TrimPrefix(path, dir+"/"))
		return true
	})
	// Compared as entries, not as a joined string: "uhai" at the end of one
	// has no trailing space, so a substring check missed it — and a mutation
	// that stopped making paths relative slipped past because of that.
	if slices.Contains(seen, "uhai") {
		t.Errorf("the root binary should be ignored: %v", seen)
	}
	if !slices.Contains(seen, "cmd/uhai/main.go") {
		t.Errorf("cmd/uhai is a different thing and must be walked: %v", seen)
	}
}

// A project with no .gitignore still gets the baseline: .git, node_modules and
// vendor are never worth walking whatever the project says.
func TestTheBaselineHoldsWithoutAGitignore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "a.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	var seen []string
	walk(context.Background(), dir, dir, func(path string) bool {
		seen = append(seen, path)
		return true
	})
	if len(seen) != 1 || !strings.HasSuffix(seen[0], "main.go") {
		t.Errorf("want only main.go, got %v", seen)
	}
}

// One process, two projects, and nothing may cross between them.
//
// This is the bug the root exists for. The daemon serves every project on the
// machine from the single directory it was spawned in — whichever project
// happened to need a daemon first — and tools resolved their relative paths
// against that. So a turn in project B read, wrote and ran commands in project
// A's tree, and nothing said so: every path existed, in the wrong repo.
func TestEachProjectsToolsStayInItsOwnTree(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "note.txt"), []byte("project A"), 0644)
	os.WriteFile(filepath.Join(b, "note.txt"), []byte("project B"), 0644)

	read := func(root string) string {
		out, isErr := Execute(context.Background(), root, NameRead, json.RawMessage(`{"path":"note.txt"}`))
		if isErr {
			t.Fatalf("read in %s: %s", root, out)
		}
		return out
	}
	if got := read(a); !strings.Contains(got, "project A") {
		t.Fatalf("project A read the wrong tree: %q", got)
	}
	if got := read(b); !strings.Contains(got, "project B") {
		t.Fatalf("project B read the wrong tree: %q", got)
	}

	// Writing is the half that does damage, so it is the half worth naming.
	if out, isErr := Execute(context.Background(), b, NameWrite,
		json.RawMessage(`{"path":"new.txt","content":"written in B"}`)); isErr {
		t.Fatalf("write in B: %s", out)
	}
	if _, err := os.Stat(filepath.Join(a, "new.txt")); err == nil {
		t.Fatal("a write for project B landed in project A")
	}
	if _, err := os.Stat(filepath.Join(b, "new.txt")); err != nil {
		t.Fatalf("the write did not land in project B: %v", err)
	}
}

// run_bash is rooted the same way, and by the shell's own working directory
// rather than by rewriting the command: a command is the user's text, and a
// tool that edits it is a tool nobody can predict.
func TestRunBashRunsInTheProject(t *testing.T) {
	dir := t.TempDir()
	out, isErr := Execute(context.Background(), dir, NameBash, json.RawMessage(`{"command":"pwd"}`))
	if isErr {
		t.Fatalf("bash: %s", out)
	}
	// macOS hands out /var symlinks for a temp dir, so compare what the shell
	// resolves rather than the string the test was given.
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(out)); got != want {
		t.Fatalf("run_bash ran in %q, want %q", got, want)
	}
}

// A search reports what the model can hand straight back to read_file. The
// walk is absolute now, and results that came back absolute would be a
// different string for every machine, longer, and wrong for a pattern the
// model writes as a project path.
func TestSearchesReportProjectPaths(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal"), 0755)
	os.WriteFile(filepath.Join(dir, "internal", "main.go"), []byte("package main // needle\n"), 0644)

	out, isErr := Execute(context.Background(), dir, NameGlob, json.RawMessage(`{"pattern":"internal/**/*.go"}`))
	if isErr || !strings.Contains(out, "internal/main.go") || strings.Contains(out, dir) {
		t.Fatalf("glob must match and report the project path, got %q", out)
	}

	out, isErr = Execute(context.Background(), dir, NameGrep, json.RawMessage(`{"pattern":"needle"}`))
	if isErr || !strings.Contains(out, "internal/main.go:1:") || strings.Contains(out, dir) {
		t.Fatalf("grep must report the project path, got %q", out)
	}
}

// No language server is the ordinary state of most machines, and it must read
// as an answer rather than as a failure: the model is told what to use instead
// and the turn goes on. A tool that errors here would cost a turn to say
// nothing, on every project that has no server installed.
func TestFindSymbolWithoutAServerSendsTheModelToGrep(t *testing.T) {
	dir := t.TempDir() // no go.mod, no package.json: nothing claims a language
	out, isErr := Execute(context.Background(), dir, NameFindSymbol, json.RawMessage(`{"name":"Whatever"}`))
	if isErr {
		t.Fatalf("a missing language server is not an error: %s", out)
	}
	if !strings.Contains(out, "grep") {
		t.Fatalf("the model has to be told what to use instead, got %q", out)
	}
}

func TestFindSymbolRefusesAnEmptyName(t *testing.T) {
	if out, isErr := Execute(context.Background(), t.TempDir(), NameFindSymbol, json.RawMessage(`{"name":"  "}`)); !isErr {
		t.Fatalf("an empty name is a bad call, not a search: %q", out)
	}
}

// run is the short way to call a tool the way the agent does.
func run(t *testing.T, root, name string, args map[string]any) (string, bool) {
	t.Helper()
	in, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return Execute(context.Background(), root, name, in)
}

// The premise of list_directory, checked rather than asserted: walk hands its
// visitor files only, so glob cannot name a directory however the pattern is
// written. If that ever changes this test fails and the tool is redundant.
func TestGlobCannotNameADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "tools", "tool.go"), []byte("package tools\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, pattern := range []string{"*", "internal/*", "**", "**/tools"} {
		got, _ := run(t, dir, NameGlob, map[string]any{"pattern": pattern})
		for _, line := range strings.Split(got, "\n") {
			if line == "internal" || line == "internal/tools" {
				t.Errorf("glob %q named a directory: %q", pattern, got)
			}
		}
	}
}

func TestListDirectoryMarksFoldersAndObeysTheProjectsIgnores(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"cmd", "node_modules", "dist"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "uhai", ".gitignore"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("/uhai\ndist/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, isErr := run(t, dir, NameList, nil)
	if isErr {
		t.Fatalf("listing a folder must not fail: %q", got)
	}
	lines := strings.Split(got, "\n")
	want := []string{".gitignore", "cmd/", "go.mod"}
	if !slices.Equal(lines, want) {
		t.Errorf("listed %q, want %q", lines, want)
	}
	// node_modules is never walked, dist/ and /uhai are the project's own
	// rules, and the trailing slash is the only thing telling the two kinds
	// of entry apart.
	for _, gone := range []string{"node_modules", "dist", "uhai"} {
		if slices.Contains(lines, gone) || slices.Contains(lines, gone+"/") {
			t.Errorf("%s should not have been listed: %q", gone, got)
		}
	}
}

// A folder whose every entry is ignored is not an empty folder, and saying so
// is the difference between the model looking again and the model believing
// there is nothing there.
func TestAnIgnoredFolderDoesNotClaimToBeEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hidden", "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, isErr := run(t, dir, NameList, map[string]any{"path": "hidden"})
	if isErr {
		t.Fatalf("an ignored folder is not an error: %q", got)
	}
	if !strings.Contains(got, "ignored") {
		t.Errorf("the note has to say the folder may not really be empty, got %q", got)
	}
}

func TestListDirectorySaysWhyItCannotList(t *testing.T) {
	got, isErr := run(t, t.TempDir(), NameList, map[string]any{"path": "nowhere"})
	if !isErr {
		t.Errorf("a folder that is not there is an error the model can act on, got %q", got)
	}
}

func TestSetPlanDrawsTheStatuses(t *testing.T) {
	got, isErr := run(t, "", NamePlan, map[string]any{"steps": []map[string]string{
		{"step": "read the package", "status": "done"},
		{"step": "add the tool", "status": "doing"},
		{"step": "update the docs"},
	}})
	if isErr {
		t.Fatalf("a valid plan must not fail: %q", got)
	}
	for _, want := range []string{"[x] read the package", "[>] add the tool", "[ ] update the docs", "1/3 done"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan has to contain %q, got %q", want, got)
		}
	}
}

// A status the tool cannot draw is refused rather than drawn as a blank box:
// silently turning "blocked" into "todo" is the model being told its plan says
// something it does not.
func TestSetPlanRefusesWhatItCannotDraw(t *testing.T) {
	got, isErr := run(t, "", NamePlan, map[string]any{"steps": []map[string]string{
		{"step": "wait for the key", "status": "blocked"},
	}})
	if !isErr {
		t.Fatalf("an unknown status must be refused, got %q", got)
	}
	if !strings.Contains(got, "todo, doing or done") {
		t.Errorf("the refusal has to name the three, got %q", got)
	}

	if got, isErr := run(t, "", NamePlan, map[string]any{"steps": []map[string]string{}}); !isErr {
		t.Errorf("an empty plan is not a plan, got %q", got)
	}
}

// No key is the ordinary state of most machines. Finding out must not cost the
// turn, so it comes back as an answer with instructions, not as an error the
// model retries around.
func TestSearchWithoutAKeyIsAnAnswer(t *testing.T) {
	t.Setenv(SearchKeyEnv, "")

	got, isErr := run(t, "", NameSearch, map[string]any{"query": "go 1.25 release notes"})
	if isErr {
		t.Errorf("a missing key is not an error: %q", got)
	}
	for _, want := range []string{"no search key", "Do not try again", "BRAVE_API_KEY"} {
		if !strings.Contains(got, want) {
			t.Errorf("the note has to say %q, got %q", want, got)
		}
	}
}

func TestSearchReadsTheResults(t *testing.T) {
	var gotQuery, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotToken = r.Header.Get("X-Subscription-Token")
		fmt.Fprint(w, `{"web":{"results":[
			{"title":"Go 1.25 <strong>Release</strong> Notes","url":"https://go.dev/doc/go1.25","description":"What&#39;s new in <strong>Go 1.25</strong>."}
		]}}`)
	}))
	defer server.Close()

	braveEndpoint = server.URL
	defer func() { braveEndpoint = "https://api.search.brave.com/res/v1/web/search" }()
	t.Setenv(SearchKeyEnv, "BSA-test")

	got, isErr := run(t, "", NameSearch, map[string]any{"query": "go 1.25 release notes"})
	if isErr {
		t.Fatalf("a good answer must not be an error: %q", got)
	}
	if gotQuery != "go 1.25 release notes" {
		t.Errorf("the service was asked %q", gotQuery)
	}
	if gotToken != "BSA-test" {
		t.Errorf("the key goes in the header, got %q", gotToken)
	}
	// Brave wraps the matched words in <strong> and escapes the rest; both
	// are markup the model pays for and cannot use.
	for _, want := range []string{"Go 1.25 Release Notes", "https://go.dev/doc/go1.25", "What's new in Go 1.25."} {
		if !strings.Contains(got, want) {
			t.Errorf("the result has to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "<strong>") {
		t.Errorf("markup survived: %q", got)
	}
}

// The service's own sentence is the only thing that says whether this is an
// expired key or a rate limit, and they need different things done about them.
func TestSearchKeepsTheServicesOwnRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"detail":"plan quota exceeded"}}`)
	}))
	defer server.Close()

	braveEndpoint = server.URL
	defer func() { braveEndpoint = "https://api.search.brave.com/res/v1/web/search" }()
	t.Setenv(SearchKeyEnv, "BSA-test")

	got, isErr := run(t, "", NameSearch, map[string]any{"query": "anything"})
	if !isErr {
		t.Fatal("a refused search is an error")
	}
	if !strings.Contains(got, "plan quota exceeded") {
		t.Errorf("the service's own words have to survive, got %q", got)
	}
}

// UHAI_API_KEY fills in every provider's key field in config, which is right
// for a gateway and would be a leak here: the key paying for the conversation
// sent to a search engine that never asked for one. Checked against the
// default reader, which is what this package ships; config's replacement is
// held to the same thing in its own test.
func TestTheConversationsKeyIsNotSentToTheSearchEngine(t *testing.T) {
	t.Setenv("UHAI_API_KEY", "sk-the-expensive-one")
	t.Setenv(SearchKeyEnv, "")

	if key := SearchKey(); key != "" {
		t.Errorf("SearchKey answered %q; it must not read the wildcard", key)
	}
}

// A command killed by its deadline has usually printed the interesting half
// already — a suite says what failed and then keeps going. Returning the
// sentence alone threw that away, which is the one thing a timed-out test run
// is good for.
func TestBashKeepsWhatItPrintedWhenKilled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	in, _ := json.Marshal(map[string]string{"command": "echo HELLO; sleep 5; echo NEVER"})
	out, isErr := Execute(ctx, "", NameBash, in)
	if !isErr {
		t.Fatalf("a killed command is an error: %s", out)
	}
	if !strings.Contains(out, "HELLO") {
		t.Errorf("the output it had already printed was dropped: %q", out)
	}
	if strings.Contains(out, "NEVER") {
		t.Errorf("it was not actually killed: %q", out)
	}
}

// A line longer than the read buffer used to end the loop with an error nobody
// read, and then block in Wait until the deadline because the pipe had stopped
// being drained: a 2 MiB line returned nothing at all and hung for the whole
// timeout. Both halves are checked here — that it finishes early, and that what
// came after the long line survived.
func TestBashSurvivesALineLongerThanTheBuffer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	out, err := Shell(ctx, "", "head -c 2000000 /dev/zero | tr '\\0' 'x'; echo; echo DONE", func(string) {})
	if err != nil {
		t.Fatalf("a long line is not a failure: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("it hung on the long line: %s", elapsed)
	}
	if !strings.Contains(out, "DONE") {
		t.Errorf("everything after the long line was lost: %.120q", out)
	}
}

// Output has a ceiling now, and it is the tail that is kept. There was none:
// one second of `yes` put 868 MiB in memory, and in the daemon that is every
// project's conversation.
func TestBashOutputHasACeiling(t *testing.T) {
	out, err := Shell(context.Background(), "", "seq 1 20000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxBashOutput+64 { // +64 for the line saying what was dropped
		t.Errorf("output is %d bytes, over the %d ceiling", len(out), maxBashOutput)
	}
	if !strings.Contains(out, "20000") {
		t.Errorf("the tail is what must be kept, and the last line is missing: %.120q", out)
	}
	if strings.Contains(out, "\n1\n") {
		t.Errorf("the head should have been dropped, not kept: %.120q", out)
	}
	if !strings.Contains(out, "dropped") {
		t.Errorf("silence reads as 'this is all of it': %.120q", out)
	}
}

// The two variables uhai alone owns are kept from a command: one `env` put
// them in a tool result, and from there into the history, the provider, and the
// session file. A vendor's variable is deliberately left alone — it is the
// user's environment, the project may need it, and stripping it would produce
// an authentication failure that nothing explains.
func TestBashHidesTheKeysUhaiOwns(t *testing.T) {
	t.Setenv("UHAI_API_KEY", "uhai-must-not-appear")
	t.Setenv(SearchKeyEnv, "bsa-must-not-appear")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-left-alone")

	out, err := Shell(context.Background(), "", "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "must-not-appear") {
		t.Error("a variable only uhai uses reached the command")
	}
	if !strings.Contains(out, "sk-ant-left-alone") {
		t.Error("a vendor variable is the user's own and must still be inherited")
	}
	if !strings.Contains(out, "NO_COLOR=1") {
		t.Error("a command is not being told there is no terminal here")
	}
}

// grep refuses a file with a NUL byte in it; run_bash had no equivalent, so
// `cat` of a binary spent a turn's tokens on mojibake.
func TestBashRefusesBinaryOutput(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": `printf 'a\000b'`})
	out, isErr := Execute(context.Background(), "", NameBash, in)
	if !isErr || !strings.Contains(out, "binary") {
		t.Fatalf("binary output should be refused with a reason: %q", out)
	}
}

// Two minutes was the only answer, and a build slower than that could not be
// verified by the model at all: /check is the other way to run something long
// and is deliberately not offered to it. The ceiling is /check's own, so nothing
// new is reachable — only reachable from here.
func TestBashTimeoutCanBeAskedForAndIsCapped(t *testing.T) {
	for input, want := range map[string]time.Duration{
		`{"command":"x"}`:                BashTimeout,     // asked for nothing
		`{"command":"x","timeout":0}`:    BashTimeout,     // asked for nothing, explicitly
		`{"command":"x","timeout":-5}`:   BashTimeout,     // nonsense is not a short timeout
		`{"command":"x","timeout":300}`:  5 * time.Minute, // asked, and allowed
		`{"command":"x","timeout":600}`:  maxBashTimeout,  // exactly the ceiling
		`{"command":"x","timeout":9999}`: maxBashTimeout,  // clamped, not refused
		`not json at all`:                BashTimeout,     // and never zero, which would kill instantly

		// A Duration is int64 nanoseconds, so anything above about 9.2e9 seconds
		// overflows when it is multiplied — and 9999999999 is a hallucinated
		// number, not an exotic one. The product wrapped negative, min chose it,
		// and the command was killed the instant it started, reporting "killed
		// after -2346317h47m54s". Clamped in seconds now, before the multiply.
		`{"command":"x","timeout":9999999999}`:          maxBashTimeout,
		`{"command":"x","timeout":10000000000}`:         maxBashTimeout,
		`{"command":"x","timeout":9223372036854775807}`: maxBashTimeout,
	} {
		if got := BashLimit(input); got != want {
			t.Errorf("%s → %s, want %s", input, got, want)
		}
	}
	if maxBashTimeout != 10*time.Minute {
		t.Errorf("the ceiling is meant to be /check's ten minutes, got %s", maxBashTimeout)
	}

	// Whatever is asked for, the answer has to be a runnable limit. A
	// non-positive one is the dangerous shape rather than merely a wrong one:
	// context.WithTimeout reads it as a deadline already past and kills the
	// command at once, which the model sees as a command that cannot be run
	// rather than as a number it should not have sent.
	for _, timeout := range []string{"1", "600", "601", "9999999999", "9223372036854775807"} {
		got := BashLimit(`{"command":"x","timeout":` + timeout + `}`)
		if got <= 0 || got > maxBashTimeout {
			t.Errorf("timeout %s → %s, which is not a runnable limit", timeout, got)
		}
	}
}

// A shorter timeout is honoured, and the message names the limit that was
// actually applied rather than the default — a model told "killed after 2m0s"
// when it asked for one second learns the wrong thing about its own request.
func TestBashReportsTheLimitItWasGiven(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"command": "echo HELLO; sleep 5", "timeout": 1})

	start := time.Now()
	out, isErr := Execute(context.Background(), "", NameBash, in)
	if !isErr {
		t.Fatalf("it should have been killed: %s", out)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the short timeout was ignored: %s", elapsed)
	}
	if !strings.Contains(out, "1s") {
		t.Errorf("the message must name the limit that applied: %q", out)
	}
	if !strings.Contains(out, "HELLO") {
		t.Errorf("and still keep what was printed: %q", out)
	}
}

// A trailing & does not detach, and the tool's description has to say so: the
// buffer is an io.Writer, so the child inherits a pipe that Wait waits on, and a
// backgrounded job holds it open for its whole life. `npm run dev &` therefore
// hangs the turn for the entire timeout and is then killed with the group — on a
// command that looks like it returns at once.
func TestATrailingAmpersandStillWaitsUnlessRedirected(t *testing.T) {
	start := time.Now()
	if _, err := Shell(context.Background(), "", "sleep 1 & echo started", nil); err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	if waited < 900*time.Millisecond {
		t.Fatalf("this test exists because & does not detach; it returned in %s", waited)
	}

	// Redirecting the job's output releases the pipe, and then it does detach.
	start = time.Now()
	if _, err := Shell(context.Background(), "", "sleep 5 >/dev/null 2>&1 & echo started", nil); err != nil {
		t.Fatal(err)
	}
	if free := time.Since(start); free > 900*time.Millisecond {
		t.Errorf("a redirected job should not be waited for, took %s", free)
	}

	// Which is the difference the model is told about, since nothing else would
	// tell it: the command looks instant either way.
	if !strings.Contains(bashTool{}.Description(), "&") {
		t.Error("the description must warn that & alone still waits")
	}
}
