// Package tools defines the tools the model may call (read_file, write_file,
// edit_file, glob, grep, run_bash, fetch_url) and runs them. It knows nothing about which provider is in use —
// the tools behave identically for every vendor.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

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

	Run(context.Context, json.RawMessage) (string, bool)
}

// tool is the plain implementation, and the only one uhai ships: data plus a
// function. The five methods below exist once, not once per tool, which is
// what keeps a built-in tool a single struct literal.
//
// The name is written once, in the literal. It used to appear in Definitions,
// again in a switch, and again in NeedsConfirm — three strings that only
// happened to match, which is where three of yesterday's bugs came from.
type tool struct {
	name        string
	description string
	schema      json.RawMessage
	confirm     bool
	run         func(context.Context, json.RawMessage) (string, bool)
}

var _ Tool = tool{}

func (t tool) Name() string            { return t.name }
func (t tool) Description() string     { return t.description }
func (t tool) Schema() json.RawMessage { return t.schema }
func (t tool) NeedsConfirm() bool      { return t.confirm }

func (t tool) Run(ctx context.Context, input json.RawMessage) (string, bool) {
	return t.run(ctx, input)
}

// noCtx adapts the tools that cannot be interrupted halfway: reading a file is
// over before a cancel could arrive, so those handlers never took a context.
func noCtx(f func(json.RawMessage) (string, bool)) func(context.Context, json.RawMessage) (string, bool) {
	return func(_ context.Context, input json.RawMessage) (string, bool) { return f(input) }
}

// Register adds a tool from outside this package. It refuses a name already in
// use rather than shadowing it: two tools answering to one name is a bug the
// model would experience as the wrong thing happening, with nothing to read.
func Register(t Tool) error {
	if _, taken := find(t.Name()); taken {
		return fmt.Errorf("a tool called %q is already registered", t.Name())
	}
	all = append(all, t)
	return nil
}

var all = []Tool{
	tool{
		name:        "read_file",
		run:         noCtx(readFile),
		description: "Read a file from disk and return its full contents as text.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Relative or absolute path to the file"}
				},
				"required": ["path"]
			}`),
	},
	tool{
		name:        "write_file",
		confirm:     true,
		run:         noCtx(writeFile),
		description: "Write (overwrite) content to a file, creating it and its parent folders if needed.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path of the file to write"},
					"content": {"type": "string", "description": "Full content to write into the file"}
				},
				"required": ["path", "content"]
			}`),
	},
	tool{
		name:        "edit_file",
		confirm:     true,
		run:         noCtx(editFile),
		description: "Replace one exact piece of text in a file. Use this instead of write_file for changing part of an existing file. The old text must appear exactly once.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path of the file to edit"},
					"old": {"type": "string", "description": "Exact text to replace, including indentation. Add surrounding lines until it is unique in the file"},
					"new": {"type": "string", "description": "Text to put in its place"}
				},
				"required": ["path", "old", "new"]
			}`),
	},
	tool{
		name:        "glob",
		run:         noCtx(globFiles),
		description: "List files whose name matches a pattern, such as *.go or **/*_test.go. Faster than run_bash for finding files, and it never needs permission.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Name pattern, e.g. \"*.go\" or \"**/*_test.go\""},
					"path": {"type": "string", "description": "Folder to search in, default the working directory"}
				},
				"required": ["pattern"]
			}`),
	},
	tool{
		name:        "grep",
		run:         noCtx(grepFiles),
		description: "Search file contents with a regular expression and return matching lines as path:line:text. Use this instead of run_bash for searching; it never needs permission.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Go regular expression to search for"},
					"path": {"type": "string", "description": "Folder to search in, default the working directory"},
					"include": {"type": "string", "description": "Only search files whose name matches this pattern, e.g. \"*.go\""}
				},
				"required": ["pattern"]
			}`),
	},
	tool{
		name:        "run_bash",
		confirm:     true,
		run:         runBash,
		description: "Run one shell (bash) command and return its stdout+stderr. It is killed after two minutes, so it must not wait for input.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell command to run"}
				},
				"required": ["command"]
			}`),
	},
	tool{
		name:    "fetch_url",
		confirm: true,
		run:     fetchURL,
		description: "Fetch a web page or document over http(s) and return it as text. " +
			"Use it to read documentation, a changelog, or an API reference the answer depends on. " +
			"Markup is stripped; only addresses on the public internet can be reached.",
		schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "The http or https URL to fetch"}
				},
				"required": ["url"]
			}`),
	},
}

// find is the one lookup. A name nobody defined finds nothing, which is what
// makes an unknown tool an answer rather than a panic.
func find(name string) (Tool, bool) {
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
