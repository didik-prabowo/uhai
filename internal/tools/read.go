// Reading a file. The one tool the model reaches for most, and one of the
// three that never has to ask.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
)

func read(input json.RawMessage) (string, bool) {
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

// readTool is how the model is told about read_file.
var readTool = tool{
	name:        "read_file",
	run:         noCtx(read),
	description: "Read a file from disk and return its full contents as text.",
	schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Relative or absolute path to the file"}
			},
			"required": ["path"]
		}`),
}
