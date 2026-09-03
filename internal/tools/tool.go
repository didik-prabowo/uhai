// Package tools defines the tools the model may call (read_file, write_file,
// edit_file, glob, grep, run_bash) and runs them. It knows nothing about which provider is in use —
// the tools behave identically for every vendor.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

const maxResultLen = 8000

// Definitions returns the ToolSpecs sent to the provider so the model knows
// what it can call.
func Definitions() []provider.ToolSpec {
	return []provider.ToolSpec{
		{
			Name:        "read_file",
			Description: "Read a file from disk and return its full contents as text.",
			JSONSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Relative or absolute path to the file"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "write_file",
			Description: "Write (overwrite) content to a file, creating it and its parent folders if needed.",
			JSONSchema: json.RawMessage(`{
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
			Description: "Replace one exact piece of text in a file. Use this instead of write_file for changing part of an existing file. The old text must appear exactly once.",
			JSONSchema: json.RawMessage(`{
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
			Description: "List files whose name matches a pattern, such as *.go or **/*_test.go. Faster than run_bash for finding files, and it never needs permission.",
			JSONSchema: json.RawMessage(`{
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
			Description: "Search file contents with a regular expression and return matching lines as path:line:text. Use this instead of run_bash for searching; it never needs permission.",
			JSONSchema: json.RawMessage(`{
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
			Description: "Run one shell (bash) command and return its stdout+stderr. It is killed after two minutes, so it must not wait for input.",
			JSONSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell command to run"}
				},
				"required": ["command"]
			}`),
		},
	}
}

// Execute runs one tool by name and returns its output as text (truncated if
// too long) plus an error flag.
func Execute(ctx context.Context, name string, input json.RawMessage) (result string, isError bool) {
	switch name {
	case "read_file":
		result, isError = readFile(input)
	case "write_file":
		result, isError = writeFile(input)
	case "edit_file":
		result, isError = editFile(input)
	case "glob":
		result, isError = globFiles(input)
	case "grep":
		result, isError = grepFiles(input)
	case "run_bash":
		result, isError = runBash(ctx, input)
	default:
		return fmt.Sprintf("unknown tool: %s", name), true
	}

	if len(result) > maxResultLen {
		result = result[:maxResultLen] + "\n...[output truncated]"
	}
	return result, isError
}
