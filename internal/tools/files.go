// Reading and changing files. Every failure here comes back as text the model
// can act on rather than as a silent empty result.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return fmt.Sprintf("could not read file: %v", err), true
	}
	return string(data), false
}

func writeFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if dir := filepath.Dir(args.Path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Sprintf("could not create folder: %v", err), true
		}
	}
	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		return fmt.Sprintf("could not write file: %v", err), true
	}
	return fmt.Sprintf("OK, wrote %d bytes to %s", len(args.Content), args.Path), false
}

func editFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if args.Old == "" {
		return "old must not be empty — use write_file to create a file", true
	}

	data, err := os.ReadFile(args.Path)
	if err != nil {
		return fmt.Sprintf("could not read file: %v", err), true
	}

	// An ambiguous match is refused rather than guessed: replacing the wrong
	// one of several matches silently corrupts the file.
	switch n := strings.Count(string(data), args.Old); n {
	case 0:
		return "the old text is not in the file — read it again, it may have changed", true
	case 1:
	default:
		return fmt.Sprintf("the old text appears %d times — include surrounding lines to make it unique", n), true
	}

	updated := strings.Replace(string(data), args.Old, args.New, 1)
	if err := os.WriteFile(args.Path, []byte(updated), 0644); err != nil {
		return fmt.Sprintf("could not write file: %v", err), true
	}
	return fmt.Sprintf("OK, edited %s", args.Path), false
}

// readFileTool is how the model is told about read_file.
var readFileTool = tool{
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
}

// writeFileTool is how the model is told about write_file.
var writeFileTool = tool{
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
}

// editFileTool is how the model is told about edit_file.
var editFileTool = tool{
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
}
