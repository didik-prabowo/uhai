// Which server answers for which language, and which languages a project is
// written in.
package lsp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// server is the command that speaks a language, and the file that says a
// project is written in it.
//
// The marker matters as much as the command. A question like "who calls
// UseProviderOn" names no file, so nothing in the request says which server to
// ask — and starting every server uhai knows about, on the chance one of them
// has heard of the name, would spend a minute indexing to answer a question
// about one language. The marker is how a person decides too: a go.mod means
// this is a Go project.
type server struct {
	lang   string
	argv   []string
	marker string   // the file that says a project uses this language
	exts   []string // and the files the language is written in
}

// servers is the table, short on purpose.
//
// zero ships fifty entries. Every one past the first is a claim that a command
// spelled that way, with those arguments, answers the protocol — and the only
// one that can be run from here is gopls. The rest are the community-standard
// invocations and no more tested than that, which is worth saying rather than
// implying: a wrong entry fails at the PATH check or at the handshake, and
// both come back as "no server for this", which is exactly what a machine
// without the server installed says. That is the failure that teaches nobody
// anything, so the list stays short enough to be checked by hand.
//
// **Exercised here:** Go. The other three are written from their published
// invocations and have never been run by this repository.
//
// ponytail: a table rather than settings. A project whose server is not here
// has no way to say so. Add `"lsp": {...}` to settings.json when somebody
// wants a language this list does not have — the shape is one map, and the
// reason to wait is that a config key for a list nobody has asked to extend
// is machinery to undo a default.
var servers = []server{
	{lang: "go", argv: []string{"gopls", "serve"}, marker: "go.mod", exts: []string{".go"}},
	{lang: "typescript", argv: []string{"typescript-language-server", "--stdio"}, marker: "package.json",
		exts: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}},
	{lang: "python", argv: []string{"pyright-langserver", "--stdio"}, marker: "pyproject.toml",
		exts: []string{".py", ".pyi"}},
	{lang: "rust", argv: []string{"rust-analyzer"}, marker: "Cargo.toml", exts: []string{".rs"}},
}

// forProject is every language this project is written in, by the markers
// present at its root. Sorted, so the order a question is asked in does not
// depend on map iteration.
func forProject(root string) []server {
	var out []server
	for _, s := range servers {
		if _, err := os.Stat(filepath.Join(root, s.marker)); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lang < out[j].lang })
	return out
}

// forFile is the one language a path is written in, and false for a file no
// server here speaks. Used when the model names a file, which narrows a
// question that would otherwise go to every server the project has.
func forFile(path string) (server, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, s := range servers {
		for _, e := range s.exts {
			if e == ext {
				return s, true
			}
		}
	}
	return server{}, false
}
