// Walking the project, and the rules for what not to walk into. Shared by
// glob and grep, which are the same search asked two ways.
package tools

import (
	"io/fs"
	"path/filepath"
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
