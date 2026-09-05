// Finding files by name.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

func glob(input json.RawMessage) (string, bool) {
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

// globTool is how the model is told about glob.
var globTool = tool{
	name:        "glob",
	run:         noCtx(glob),
	description: "List files whose name matches a pattern, such as *.go or **/*_test.go. Faster than run_bash for finding files, and it never needs permission.",
	schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Name pattern, e.g. \"*.go\" or \"**/*_test.go\""},
				"path": {"type": "string", "description": "Folder to search in, default the working directory"}
			},
			"required": ["pattern"]
		}`),
}
