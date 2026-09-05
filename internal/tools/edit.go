// Replacing one exact piece of a file, which is what a change usually is.
// write_file for a new file, this for an existing one.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

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
