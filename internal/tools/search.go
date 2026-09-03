// Finding files and searching them, walked here rather than shelled out: no
// permission needed, and the same behaviour on every machine.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// skipDirs are never worth walking into: huge, generated, or not the user's code.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

const maxMatches = 200

// walk visits every file under root, skipping the noise directories. The
// callback stops the walk by returning false.
func walk(root string, visit func(path string) bool) error {
	if root == "" {
		root = "."
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is not a reason to fail the search
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !visit(path) {
			return filepath.SkipAll
		}
		return nil
	})
}

// matchName reports whether a path matches a name pattern. A leading "**/" is
// dropped and the rest is matched against the file name, since filepath.Match
// has no notion of "any number of folders".
//
// ponytail: no real ** support, no brace expansion. Reach for a glob library
// only if patterns like "src/**/test/*.go" actually start being needed.
func matchName(pattern, path string) bool {
	pattern = strings.TrimPrefix(pattern, "**/")
	if strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	ok, _ := filepath.Match(pattern, filepath.Base(path))
	return ok
}

func globFiles(input json.RawMessage) (string, bool) {
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

// truncate keeps a matching line readable: a minified bundle on one line would
// otherwise fill the whole result on its own.
func truncate(line string) string {
	const max = 200
	line = strings.TrimRight(line, "\r")
	if len(line) > max {
		return line[:max] + "…"
	}
	return line
}
