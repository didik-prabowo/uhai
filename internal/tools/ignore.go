package tools

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// skipDirs are never worth walking into whatever the project says: huge,
// generated, or not the user's code. Kept apart from .gitignore because a
// repository that tracks its vendor directory still does not want grep reading
// it, and because these have to hold when there is no .gitignore at all.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// ignoreList is what a project has said not to look at.
type ignoreList struct {
	root  string
	match []func(rel string) bool
	dirs  map[string]bool
}

// readIgnore reads .gitignore at the root of the search.
//
// The common forms and no more: a bare name, a trailing slash for
// directory-only, a leading slash to anchor at the root, and the globs matcher
// already understands. Not negation, not a .gitignore per subdirectory, not
// the precedence rules between them — that is a real implementation of a real
// specification, and zot has a package for it.
//
// Reading it at all is most of the value: a search that walks dist/, target/
// and .venv/ is slow and answers with files nobody wrote. Being wrong about a
// negation means a file gets searched anyway, which is the direction to be
// wrong in.
func readIgnore(root string) ignoreList {
	if root == "" {
		root = "."
	}
	list := ignoreList{root: root, dirs: map[string]bool{}}
	f, err := os.Open(filepath.Join(root, ".gitignore"))
	if err != nil {
		return list
	}
	defer f.Close()

	lines := bufio.NewScanner(f)
	for lines.Scan() {
		rule := strings.TrimSpace(lines.Text())
		if rule == "" || strings.HasPrefix(rule, "#") {
			continue
		}
		if strings.HasPrefix(rule, "!") {
			// A negation un-ignores something. Skipped rather than
			// half-honoured: a rule that is read but not obeyed is worse than
			// one that was never read.
			continue
		}
		if dir := strings.TrimSuffix(rule, "/"); dir != rule {
			list.dirs[strings.TrimPrefix(dir, "/")] = true
			continue
		}

		if strings.HasPrefix(rule, "/") {
			// Anchored: the root and nowhere else. "/uhai" is the built
			// binary, not every directory called uhai in the tree — which is
			// what it caught while the rules were matched against absolute
			// paths instead of paths relative to the search.
			at := strings.TrimPrefix(rule, "/")
			list.match = append(list.match, func(rel string) bool {
				return rel == at || strings.HasPrefix(rel, at+"/")
			})
			continue
		}
		if !strings.Contains(rule, "/") {
			list.dirs[rule] = true // a bare name can be a directory too
		}
		list.match = append(list.match, matcher("**/"+rule))
	}
	return list
}

// ignores reports whether a walked path was ruled out by the project. The path
// is made relative to the search first: a rule is about the project, not about
// where the project happens to sit on this machine.
func (l ignoreList) ignores(path string) bool {
	rel, err := filepath.Rel(l.root, path)
	if err != nil {
		return false
	}
	for _, m := range l.match {
		if m(rel) {
			return true
		}
	}
	return false
}
