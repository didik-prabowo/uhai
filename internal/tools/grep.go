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

// maxGrepFile is the largest file grep will read. It reads whole — the pattern
// is matched line by line, but the bytes arrive in one allocation and
// strings.Split makes a second copy of them — so the ceiling is the only thing
// between one generated file and a search that costs more memory than the
// process it runs in.
const maxGrepFile = 4 << 20

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
				"path": {"type": "string", "description": "Folder to search in, relative to the project, default the whole project"},
				"include": {"type": "string", "description": "Only search files whose name matches this pattern, e.g. \"*.go\""}
			},
			"required": ["pattern"]
		}`)
}

func (grepTool) Run(ctx context.Context, root string, input json.RawMessage) (string, bool) {
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
	var skipped, large int
	include := func(string) bool { return true }
	if args.Include != "" {
		include = matcher(args.Include)
	}
	// Walked absolutely, matched and reported relative to the project: the
	// include pattern is written as a project path, and so is every result the
	// model reads back and hands to read_file.
	err = walk(ctx, resolve(root, args.Path), func(path string) bool {
		rel := display(root, path)
		if !include(rel) {
			return true
		}
		// The same files read_file refuses. Redaction takes care of a
		// credential that has a recognisable shape wherever it lives; this is
		// for the one that does not — DB_PASSWORD=hunter2 is a matching line
		// like any other, and .env is the file it lives in.
		if SecretPath(rel) {
			skipped++
			return true
		}
		// Read whole, so a file has to be small enough to hold whole. There was
		// no ceiling: a 300 MiB log in the tree cost 600 MiB of allocation —
		// the read, and then the copy that strings.Split makes of it — for a
		// grep that was looking at source. Four megabytes is far above any file
		// somebody wrote and far below the ones that hurt.
		if info, err := os.Stat(path); err == nil && info.Size() > maxGrepFile {
			large++
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
			hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, i+1, truncate(line)))
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
	// Said rather than silent: a search that quietly left files out reads as a
	// search that found everything, and the count is what lets the model ask
	// for one of them by name instead.
	tail := ""
	if skipped > 0 {
		tail = fmt.Sprintf("\n\n(%d file(s) skipped: credentials by convention — read one by name if you need it)", skipped)
	}
	if large > 0 {
		tail += fmt.Sprintf("\n\n(%d file(s) skipped: larger than %d MiB — run_bash grep if one of them is the answer)", large, maxGrepFile>>20)
	}
	if len(hits) == 0 {
		return "no matches for " + args.Pattern + tail, false
	}
	return strings.Join(hits, "\n") + tail, false
}
