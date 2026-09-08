// Writing a file whole. It asks first, and the question shows a diff rather
// than a blob of JSON, because a diff can be judged.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeTool is the write_file tool: what the model is told about it, and the
// thing that runs.
type writeTool struct{}

func (writeTool) Name() string       { return NameWrite }
func (writeTool) NeedsConfirm() bool { return true }
func (writeTool) Description() string {
	return "Write (overwrite) content to a file, creating it and its parent folders if needed."
}
func (writeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Path of the file to write"},
				"content": {"type": "string", "description": "Full content to write into the file"}
			},
			"required": ["path", "content"]
		}`)
}

func (writeTool) Run(_ context.Context, root string, input json.RawMessage) (string, bool) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	path := resolve(root, args.Path)
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Sprintf("could not create folder: %v", err), true
		}
	}
	if err := os.WriteFile(path, []byte(args.Content), 0644); err != nil {
		return fmt.Sprintf("could not write file: %v", err), true
	}
	return fmt.Sprintf("OK, wrote %d bytes to %s", len(args.Content), args.Path), false
}
