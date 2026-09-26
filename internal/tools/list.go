// Listing one folder. The gap `glob` left: `walk` only ever calls its visitor
// for files — a directory entry is either descended into or skipped — so no
// search tool here can name a directory. A model wanting to know the shape of
// a tree had `run_bash ls` and nothing else, which asks permission for the
// least dangerous thing in the project.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// listTool is the list_directory tool: what the model is told about it, and
// the thing that runs.
type listTool struct{}

func (listTool) Name() string       { return NameList }
func (listTool) NeedsConfirm() bool { return false }
func (listTool) Description() string {
	return "List the entries of one folder, directories marked with a trailing slash. One level only — use glob for a pattern at any depth."
}
func (listTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Folder to list, relative to the project, default the project itself"}
			}
		}`)
}

func (listTool) Run(_ context.Context, root string, input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	// An empty input is a valid call here — every field is optional — and
	// Unmarshal refuses "" outright, so only real JSON is offered to it.
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return err.Error(), true
		}
	}

	dir := resolve(root, args.Path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("could not list folder: %v", err), true
	}

	// Read at the project rather than at the folder being listed: .gitignore
	// sits at the root and its rules are about the project, so listing
	// internal/ has to consult the same file listing the root does.
	base := root
	if base == "" {
		base = dir
	}
	ignore := readIgnore(base)

	var out []string
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		if e.IsDir() {
			if skipDirs[name] || ignore.dirs[name] || ignore.ignores(path) {
				continue
			}
			out = append(out, name+"/")
			continue
		}
		if ignore.ignores(path) {
			continue
		}
		out = append(out, name)
		if len(out) >= maxMatches {
			return strings.Join(out, "\n") + fmt.Sprintf("\n\n(first %d entries; there are more)", maxMatches), false
		}
	}

	if len(out) == 0 {
		// Said as "nothing to show" rather than "empty": what was hidden by
		// .gitignore was really there, and a model told the folder is empty
		// stops looking.
		return "nothing to list in " + display(root, dir) + " (ignored files are not shown)", false
	}
	return strings.Join(out, "\n"), false
}
