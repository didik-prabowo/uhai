package tools

import (
	"context"
	"encoding/json"
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
