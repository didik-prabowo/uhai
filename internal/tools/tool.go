// Package tools defines the tools the model may call (read_file, write_file,
// edit_file, glob, grep, list_directory, run_bash, fetch_url, search_web,
// find_symbol, set_plan) and runs them. It knows nothing about which provider is in use —
// the tools behave identically for every vendor.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/didik-prabowo/uhai/internal/provider"
)

const maxResultLen = 8000

// Tool is one tool, whole: what the model is told about it, whether it has to
// ask before running, and the thing that runs.
//
// It is an interface so a tool need not live in this package. Everything uhai
// ships is built from the `tool` struct below, which is one line each; the
// interface is what lets a tool arrive from somewhere else — an MCP server, a
// plugin — whose name is not known until the program is running, and which a
// slice literal compiled into this file could never hold.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage

	// NeedsConfirm reports whether the tool's effects escape this process —
	// writing to disk, running commands, leaving the machine.
	NeedsConfirm() bool

	// Run does the work and returns what the model should read.
	//
	// root is the project the call is about, and every relative path in input
	// is resolved against it rather than against the process's own directory.
	// A tool with nothing on disk to touch ignores it.
	//
	// It is an argument rather than a package-level setting because one
	// process serves several projects at once: the daemon holds a workspace
	// per project and their turns run together, so a root stored anywhere but
	// on the call would be read by the wrong one.
	//
	// isError is not "the function failed" — it is the is_error flag on the
	// tool_result block that goes back over the wire, and result is what the
	// model reads either way. A tool that cannot do its job has still done
	// its job by saying why: "could not read file: no such file" is a fact
	// the model can act on, where a dropped result leaves it believing the
	// file was empty. That is why this is not (string, error).
	Run(ctx context.Context, root string, input json.RawMessage) (result string, isError bool)
}

// mu guards all. Registration is expected at startup, but the whole point of
// the interface is tools that arrive from somewhere else — an MCP server that
// reconnects mid-session would register from its own goroutine while the agent
// is reading the list from its. database/sql.Register and http.ServeMux lock
// for the same reason: a registry that is only safe when used the way its
// author imagined is not safe.
var mu sync.RWMutex

// Register adds a tool from outside this package. It refuses a name already in
// use rather than shadowing it: two tools answering to one name is a bug the
// model would experience as the wrong thing happening, with nothing to read.
func Register(t Tool) error {
	mu.Lock()
	defer mu.Unlock()
	for _, have := range all {
		if have.Name() == t.Name() {
			return fmt.Errorf("a tool called %q is already registered", t.Name())
		}
	}
	all = append(all, t)
	return nil
}

// all is every tool uhai ships. Each one is defined beside the function that
// runs it — read_file in files.go, run_bash in shell.go — so changing a tool's
// description and changing its behaviour are the same file. This is the list
// rather than seven init() calls: the order here is the order the model is
// given, and that order is part of the cached prefix, so it should be written
// down rather than left to whatever order the files happen to initialise in.
var all = []Tool{
	readTool{},
	writeTool{},
	editTool{},
	globTool{},
	grepTool{},
	bashTool{},
	fetchTool{},
	symbolTool{},
	listTool{},
	planTool{},
	searchTool{},
}

// find is the one lookup. A name nobody defined finds nothing, which is what
// makes an unknown tool an answer rather than a panic.
func find(name string) (Tool, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, t := range all {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

// Names is every tool this package dispatches, in the order they are offered.
// It exists for the sentence a model reads when it calls something that is not
// here; spawn_task is absent for the reason it is absent from all, and a model
// that calls it never reaches Execute.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]string, 0, len(all))
	for _, t := range all {
		out = append(out, t.Name())
	}
	return out
}

// Definitions returns the ToolSpecs sent to the provider so the model knows
// what it can call.
func Definitions() []provider.ToolSpec {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]provider.ToolSpec, 0, len(all))
	for _, t := range all {
		out = append(out, provider.ToolSpec{Name: t.Name(), Description: t.Description(), JSONSchema: t.Schema()})
	}
	return out
}

// Execute runs one tool by name and returns its output as text (truncated if
// too long) plus an error flag.
func Execute(ctx context.Context, root, name string, input json.RawMessage) (result string, isError bool) {
	t, ok := find(name)
	if !ok {
		// Named, because a refusal that does not say what exists costs a whole
		// turn to guess again. Watched live: a model reached for read_file_ide —
		// a name it remembered from another harness — and was told only that it
		// was unknown, so it tried the same thing six times before arriving at
		// read_file. Seven provider calls to read one file, six of them paying
		// for the whole prefix to learn nothing.
		return fmt.Sprintf("unknown tool: %s. The tools here are: %s", name, strings.Join(Names(), ", ")), true
	}
	result, isError = t.Run(ctx, root, input)

	// Here rather than in each tool: this is the one place every result passes
	// through, so one pass covers read_file, grep, run_bash and fetch_url at
	// once. Before the truncation, so a marker cannot be cut in half.
	result = Redact(result)

	if len(result) > maxResultLen {
		result = result[:maxResultLen] + "\n...[output truncated]"
	}
	return result, isError
}

// resolve is where a path the model gave lands on disk: under the project when
// it is relative, and exactly where it says when it is absolute.
//
// It exists because the process's own directory is not the project's. One
// daemon serves every project on the machine and is started from whichever one
// happened to need it first, so a relative path resolved against the process
// read — and wrote, and ran commands in — that first project's tree for every
// project after it. Nothing said so: the paths all existed, in the wrong repo.
//
// An absolute path is left alone rather than confined. What may be touched is
// already answered by the allow/ask/deny lists, and confining here would
// quietly change what a rule someone wrote means.
func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if root == "" {
		return path // no project named: the process's own directory, as before
	}
	return filepath.Join(root, path)
}

// display is the other direction: what the model should be shown. Searches
// walk an absolute root now, and handing back absolute paths would change
// every result the model reads and pastes into its next call — for a fact it
// gains nothing from. Anything outside the project keeps its full path,
// because "../../.." is a worse answer than the truth.
func display(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
