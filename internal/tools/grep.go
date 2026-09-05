// Searching what is inside them.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

func grepFiles(input json.RawMessage) (string, bool) {
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

	var hits []string
	err = walk(args.Path, func(path string) bool {
		if args.Include != "" && !matchName(args.Include, path) {
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
	if err != nil {
		return fmt.Sprintf("could not search: %v", err), true
	}
	if len(hits) == 0 {
		return "no matches for " + args.Pattern, false
	}
	return strings.Join(hits, "\n"), false
}

// grepTool is how the model is told about grep.
var grepTool = tool{
	name:        "grep",
	run:         noCtx(grepFiles),
	description: "Search file contents with a regular expression and return matching lines as path:line:text. Use this instead of run_bash for searching; it never needs permission.",
	schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Go regular expression to search for"},
				"path": {"type": "string", "description": "Folder to search in, default the working directory"},
				"include": {"type": "string", "description": "Only search files whose name matches this pattern, e.g. \"*.go\""}
			},
			"required": ["pattern"]
		}`),
}
