// Finding files by name.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const GLOB = "glob"

// globTool is the glob tool: what the model is told about it, and the
// thing that runs.
type globTool struct{}

func (globTool) Name() string       { return GLOB }
func (globTool) NeedsConfirm() bool { return false }
func (globTool) Description() string {
	return "List files whose name matches a pattern, such as *.go or **/*_test.go. Faster than run_bash for finding files, and it never needs permission."
}
func (globTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Name pattern, e.g. \"*.go\" or \"**/*_test.go\""},
				"path": {"type": "string", "description": "Folder to search in, default the working directory"}
			},
			"required": ["pattern"]
		}`)
}

func (globTool) Run(_ context.Context, input json.RawMessage) (string, bool) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if args.Pattern == "" {
		return "pattern must not be empty", true
	}

	var found []string
	err := walk(args.Path, func(path string) bool {
		if matchName(args.Pattern, path) {
			found = append(found, path)
		}
		return len(found) < maxMatches
	})
	if err != nil {
		return fmt.Sprintf("could not search: %v", err), true
	}
	if len(found) == 0 {
		return "no files match " + args.Pattern, false
	}
	return strings.Join(found, "\n"), false
}
