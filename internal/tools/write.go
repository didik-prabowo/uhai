// Writing a file whole. It asks first, and the question shows a diff rather
// than a blob of JSON, because a diff can be judged.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func write(input json.RawMessage) (string, bool) {
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

// writeTool is how the model is told about write_file.
var writeTool = tool{
	name:        "write_file",
	confirm:     true,
	run:         noCtx(write),
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
