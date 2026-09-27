// Finding files by name.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// globTool is the glob tool: what the model is told about it, and the
// thing that runs.
type globTool struct{}

func (globTool) Name() string       { return NameGlob }
func (globTool) NeedsConfirm() bool { return false }
func (globTool) Description() string {
	return "List files whose name matches a pattern, such as *.go or **/*_test.go. Faster than run_bash for finding files, and it never needs permission."
}
func (globTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Name pattern, e.g. \"*.go\" or \"**/*_test.go\""},
				"path": {"type": "string", "description": "Folder to search in, relative to the project, default the whole project"}
			},
			"required": ["pattern"]
		}`)
}

func (globTool) Run(ctx context.Context, root string, input json.RawMessage) (string, bool) {
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

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	var found []string
	match := matcher(args.Pattern)
	// Walked absolutely, matched and reported relative to the project. The
	// pattern the model writes is a project path — "internal/**/*.go" — and it
	// would match nothing against /Users/…/internal/foo.go.
	err := walk(ctx, root, resolve(root, args.Path), func(path string) bool {
		rel := display(root, path)
		if match(rel) {
			found = append(found, rel)
		}
		return len(found) < maxMatches
	})
	note, isErr := searchNote(err, len(found))
	if isErr {
		return note, true
	}
	if err != nil && note == "" {
		return fmt.Sprintf("could not search: %v", err), true
	}
	return searchResult("no files match "+args.Pattern, found, note), false
}
