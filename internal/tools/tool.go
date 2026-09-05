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

// tool is one tool, whole: what the model is told about it, whether it has to
// ask before running, and the thing that runs.
//
// It is a table rather than an interface because tools differ in data and in
// one function, not in behaviour — five methods where four return a constant
// is ceremony around a struct literal. What matters is that the name is
// written once: it used to appear in Definitions, again in a switch, and again
// in NeedsConfirm, three strings that only happened to match. All three of the
// ways that could go wrong are now unrepresentable rather than tested for.
type tool struct {
	Name        string
	Description string
	Schema      json.RawMessage

	// Confirm marks a tool whose effects escape this process — writing to
	// disk, running commands, leaving the machine.
	Confirm bool

	Run func(context.Context, json.RawMessage) (string, bool)
}

// noCtx adapts the tools that cannot be interrupted halfway: reading a file is
// over before a cancel could arrive, so those handlers never took a context.
func noCtx(f func(json.RawMessage) (string, bool)) func(context.Context, json.RawMessage) (string, bool) {
	return func(_ context.Context, input json.RawMessage) (string, bool) { return f(input) }
}

var all = []tool{
	{
		Name:        "read_file",
		Run:         noCtx(readFile),
		Description: "Read a file from disk and return its full contents as text.",
		Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Relative or absolute path to the file"}
				},
				"required": ["path"]
			}`),
	},
	{
		Name:        "write_file",
		Confirm:     true,
		Run:         noCtx(writeFile),
		Description: "Write (overwrite) content to a file, creating it and its parent folders if needed.",
		Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path of the file to write"},
					"content": {"type": "string", "description": "Full content to write into the file"}
				},
				"required": ["path", "content"]
			}`),
	},
	{
		Name:        "edit_file",
		Confirm:     true,
		Run:         noCtx(editFile),
		Description: "Replace one exact piece of text in a file. Use this instead of write_file for changing part of an existing file. The old text must appear exactly once.",
		Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path of the file to edit"},
					"old": {"type": "string", "description": "Exact text to replace, including indentation. Add surrounding lines until it is unique in the file"},
					"new": {"type": "string", "description": "Text to put in its place"}
				},
				"required": ["path", "old", "new"]
			}`),
	},
	{
		Name:        "glob",
		Run:         noCtx(globFiles),
		Description: "List files whose name matches a pattern, such as *.go or **/*_test.go. Faster than run_bash for finding files, and it never needs permission.",
		Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Name pattern, e.g. \"*.go\" or \"**/*_test.go\""},
					"path": {"type": "string", "description": "Folder to search in, default the working directory"}
				},
				"required": ["pattern"]
			}`),
	},
	{
		Name:        "grep",
		Run:         noCtx(grepFiles),
		Description: "Search file contents with a regular expression and return matching lines as path:line:text. Use this instead of run_bash for searching; it never needs permission.",
		Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Go regular expression to search for"},
					"path": {"type": "string", "description": "Folder to search in, default the working directory"},
					"include": {"type": "string", "description": "Only search files whose name matches this pattern, e.g. \"*.go\""}
				},
				"required": ["pattern"]
			}`),
	},
	{
		Name:        "run_bash",
		Confirm:     true,
		Run:         runBash,
		Description: "Run one shell (bash) command and return its stdout+stderr. It is killed after two minutes, so it must not wait for input.",
		Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell command to run"}
				},
				"required": ["command"]
			}`),
	},
	{
		Name:    "fetch_url",
		Confirm: true,
		Run:     fetchURL,
		Description: "Fetch a web page or document over http(s) and return it as text. " +
			"Use it to read documentation, a changelog, or an API reference the answer depends on. " +
			"Markup is stripped; only addresses on the public internet can be reached.",
		Schema: json.RawMessage(`{
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
func find(name string) (tool, bool) {
	for _, t := range all {
		if t.Name == name {
			return t, true
		}
	}
	return tool{}, false
}

// Definitions returns the ToolSpecs sent to the provider so the model knows
// what it can call.
func Definitions() []provider.ToolSpec {
	out := make([]provider.ToolSpec, 0, len(all))
	for _, t := range all {
		out = append(out, provider.ToolSpec{Name: t.Name, Description: t.Description, JSONSchema: t.Schema})
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
