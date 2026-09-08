// Finding a symbol by what it is rather than by how it is spelled.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/didik-prabowo/uhai/internal/lsp"
)

// symbolTool answers the questions grep answers badly: where is this defined,
// and who uses it. grep matches text, so a method called Run collides with
// every other Run in the tree and with the word in a comment; a language
// server matches meaning, so it returns the six that are the same Run.
//
// It takes a name and not a position on purpose. The references take line and
// column, which means the model greps first and then counts characters into a
// line — the step it gets wrong, and the one it cannot check. A name is what
// it has read in the source.
type symbolTool struct{}

func (symbolTool) Name() string       { return NameFindSymbol }
func (symbolTool) NeedsConfirm() bool { return false }
func (symbolTool) Description() string {
	return "Find where a symbol is defined, who uses it, or what implements it, using the project's language server. Answers by meaning rather than by text, so it does not collide on a common name the way grep does. Give the symbol's name; no line or column is needed. Falls back with a note when no language server is available."
}
func (symbolTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "The symbol's name, exactly as written in the source, e.g. \"UseProviderOn\""},
				"what": {"type": "string", "enum": ["definition", "references", "implementations"], "description": "What to ask about it, default \"definition\""},
				"path": {"type": "string", "description": "File the symbol is in, only needed when the name is declared in more than one place"}
			},
			"required": ["name"]
		}`)
}

func (symbolTool) Run(ctx context.Context, root string, input json.RawMessage) (string, bool) {
	var args struct {
		Name string `json:"name"`
		What string `json:"what"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return "name must not be empty", true
	}
	if args.What == "" {
		args.What = "definition"
	}

	found, ok, err := lsp.Symbols(ctx, root, args.Name)
	if !ok {
		// A normal answer, not an error. No server installed for this project
		// is the ordinary state of most machines, and a turn should carry on
		// with the tool that does work rather than stop.
		return "no language server available for this project — use grep instead", false
	}
	if err != nil && len(found) == 0 {
		return fmt.Sprintf("the language server could not answer: %v — use grep instead", err), false
	}
	if len(found) == 0 {
		return fmt.Sprintf("no symbol called %q — check the spelling, or use grep for text", args.Name), false
	}

	// A name declared in several places is a question, not an answer. Handing
	// back the first one would be a guess the model cannot see being made.
	if args.Path != "" {
		found = onlyIn(found, root, args.Path)
		if len(found) == 0 {
			return fmt.Sprintf("no symbol called %q in %s", args.Name, args.Path), false
		}
	}
	if len(found) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "%q is declared in %d places — ask again with \"path\" set to one of them:\n", args.Name, len(found))
		for _, s := range found {
			fmt.Fprintf(&b, "  %s  %s%s\n", display(root, s.Path), s.Kind, container(s))
		}
		return b.String(), false
	}

	sym := found[0]
	if args.What == "definition" {
		return fmt.Sprintf("%s  %s%s", loc(root, sym.Location), sym.Kind, container(sym)), false
	}

	places, err := lsp.At(ctx, root, args.What, sym)
	if err != nil {
		return fmt.Sprintf("the language server could not answer: %v — use grep instead", err), false
	}
	if len(places) == 0 {
		return fmt.Sprintf("no %s of %s", args.What, args.Name), false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d %s of %s:\n", len(places), args.What, args.Name)
	for _, p := range places {
		fmt.Fprintf(&b, "  %s\n", loc(root, p))
	}
	return b.String(), false
}

// onlyIn keeps the declarations in one file, named the way the model named it.
func onlyIn(found []lsp.Symbol, root, path string) []lsp.Symbol {
	want := resolve(root, path)
	var out []lsp.Symbol
	for _, s := range found {
		if s.Path == want {
			out = append(out, s)
		}
	}
	return out
}

// loc is a place as the model should read it: the project's own path, so it
// can hand the result straight to read_file, and the line:column a person
// would type.
func loc(root string, l lsp.Location) string {
	return fmt.Sprintf("%s:%d:%d", display(root, l.Path), l.Line, l.Column)
}

func container(s lsp.Symbol) string {
	if s.Container == "" {
		return ""
	}
	return " in " + s.Container
}
