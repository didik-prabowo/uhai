// Reading a file. The one tool the model reaches for most, and one of the
// three that never has to ask.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// readTool is the read_file tool: what the model is told about it, and the
// thing that runs.
type readTool struct{}

func (readTool) Name() string       { return NameRead }
func (readTool) NeedsConfirm() bool { return false }
func (readTool) Description() string {
	return "Read a file from disk and return its full contents as text."
}
func (readTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Relative or absolute path to the file"}
			},
			"required": ["path"]
		}`)
}

func (readTool) Run(_ context.Context, root string, input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	data, err := os.ReadFile(resolve(root, args.Path))
	if err != nil {
		return fmt.Sprintf("could not read file: %v", err), true
	}
	return string(data), false
}
