// Searching what is inside them.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// grepTool is the grep tool: what the model is told about it, and the
// thing that runs.
type grepTool struct{}

func (grepTool) Name() string       { return NameGrep }
func (grepTool) NeedsConfirm() bool { return false }
func (grepTool) Description() string {
	return "Search file contents with a regular expression and return matching lines as path:line:text. Use this instead of run_bash for searching; it never needs permission."
}
func (grepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Go regular expression to search for"},
				"path": {"type": "string", "description": "Folder to search in, default the working directory"},
				"include": {"type": "string", "description": "Only search files whose name matches this pattern, e.g. \"*.go\""}
			},
			"required": ["pattern"]
		}`)
}

func (grepTool) Run(ctx context.Context, input json.RawMessage) (string, bool) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return fmt.Sprintf("bad regular expression: %v", err), true
	}

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	var hits []string
	include := func(string) bool { return true }
	if args.Include != "" {
		include = matcher(args.Include)
	}
	err = walk(ctx, args.Path, func(path string) bool {
		if !include(path) {
			return true
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return true // unreadable, or binary: nothing to show a human
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !re.MatchString(line) {
				continue
			}
			hits = append(hits, fmt.Sprintf("%s:%d:%s", path, i+1, truncate(line)))
			if len(hits) >= maxMatches {
				return false
			}
		}
		return true
	})
	if note, isErr := searchNote(err, len(hits)); note != "" {
		if isErr {
			return note, true
		}
		return strings.Join(hits, "\n") + "\n\n" + note, false
	}
	if err != nil {
		return fmt.Sprintf("could not search: %v", err), true
	}
	if len(hits) == 0 {
		return "no matches for " + args.Pattern, false
	}
	return strings.Join(hits, "\n"), false
}
