// Package tools defines the tools the model may call (read_file, write_file,
// edit_file, glob, grep, run_bash, fetch_url) and runs them. It knows nothing about which provider is in use —
// the tools behave identically for every vendor.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	// isError is not "the function failed" — it is the is_error flag on the
	// tool_result block that goes back over the wire, and result is what the
	// model reads either way. A tool that cannot do its job has still done
	// its job by saying why: "could not read file: no such file" is a fact
	// the model can act on, where a dropped result leaves it believing the
	// file was empty. That is why this is not (string, error).
	Run(ctx context.Context, input json.RawMessage) (result string, isError bool)
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
func Execute(ctx context.Context, name string, input json.RawMessage) (result string, isError bool) {
	t, ok := find(name)
	if !ok {
		return fmt.Sprintf("unknown tool: %s", name), true
	}
	result, isError = t.Run(ctx, input)

	if len(result) > maxResultLen {
		result = result[:maxResultLen] + "\n...[output truncated]"
	}
	return result, isError
}
