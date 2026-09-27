// Replacing exact pieces of files, which is what a change usually is.
// write_file for a new file, this for existing ones.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// editTool is the edit_file tool: what the model is told about it, and the
// thing that runs.
type editTool struct{}

func (editTool) Name() string       { return NameEdit }
func (editTool) NeedsConfirm() bool { return true }
func (editTool) Description() string {
	return "Replace exact pieces of text in files. Use this instead of write_file for changing part of an existing file. " +
		"Each old text must appear exactly once in its file. " +
		"Send several edits together in one call — across one file or many — and they are shown as one question and applied together or not at all, " +
		"which is what a rename across a dozen files wants."
}
func (editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Path of the file to edit, for a single edit"},
				"old": {"type": "string", "description": "Exact text to replace, including indentation. Add surrounding lines until it is unique in the file"},
				"new": {"type": "string", "description": "Text to put in its place"},
				"edits": {
					"type": "array",
					"description": "Several edits at once, instead of path/old/new. Applied in order, all of them or none",
					"items": {
						"type": "object",
						"properties": {
							"path": {"type": "string"},
							"old":  {"type": "string"},
							"new":  {"type": "string"}
						},
						"required": ["path", "old", "new"]
					}
				}
			}
		}`)
}

// oneEdit is a single replacement. The wire shape for both spellings, so the
// batch and the single edit are one code path after parsing.
type oneEdit struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// editArgs is either spelling. Neither field is required in the schema because
// exactly one of them has to be given, which JSON Schema can express only as a
// oneOf that not every provider honours — so it is checked here, where the
// message can say which.
type editArgs struct {
	oneEdit
	Edits []oneEdit `json:"edits"`
}

// edits returns what was asked for, whichever way it was written.
func (a editArgs) edits() ([]oneEdit, string) {
	switch {
	case len(a.Edits) > 0 && a.Path != "":
		return nil, `give either "edits" or a single "path"/"old"/"new", not both`
	case len(a.Edits) > 0:
		return a.Edits, ""
	case a.Path != "":
		return []oneEdit{a.oneEdit}, ""
	}
	return nil, `nothing to do: give "path", "old" and "new", or a list in "edits"`
}

func (editTool) Run(_ context.Context, root string, input json.RawMessage) (string, bool) {
	var args editArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	list, problem := args.edits()
	if problem != "" {
		return problem, true
	}

	// Every edit is checked against every file before any file is written, and
	// the files are held in memory until all of them pass. A half-applied
	// rename is worse than a refused one: the build breaks in a way that looks
	// like the model's last idea rather than like a tool that gave up, and
	// nothing says which half landed.
	//
	// Keyed by the resolved path, so two edits to one file are applied in order
	// and the second sees the first — which is what a refactor within a file
	// needs, and what reading the file twice from disk would not give.
	updated := map[string]string{}
	order := []string{}

	for i, e := range list {
		where := fmt.Sprintf("edit %d (%s)", i+1, e.Path)
		if len(list) == 1 {
			where = "the old text"
		}
		if e.Old == "" {
			return where + " has no old text — use write_file to create a file", true
		}

		path := resolve(root, e.Path)
		body, held := updated[path]
		if !held {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Sprintf("could not read %s: %v", e.Path, err), true
			}
			body = string(data)
			order = append(order, path)
		}

		// An ambiguous match is refused rather than guessed: replacing the
		// wrong one of several matches silently corrupts the file.
		switch n := strings.Count(body, e.Old); n {
		case 0:
			// A redaction marker in the text to replace is not a stale read,
			// and "read it again" would send the model round the same loop:
			// reading it again returns the same marker. The value is on disk
			// unchanged; it is the tool result that never carried it.
			if strings.Contains(e.Old, redactedPrefix) {
				return where + " contains a redacted credential, which is a marker and not what the file holds — " +
					"edit around it, or ask the user to make the change", true
			}
			return where + " is not in the file — read it again, it may have changed", true
		case 1:
		default:
			return fmt.Sprintf("%s appears %d times — include surrounding lines to make it unique", where, n), true
		}

		updated[path] = strings.Replace(body, e.Old, e.New, 1)
	}

	// Written only now, and a failure part-way through is reported with what
	// did land: the alternative is a model that believes nothing happened.
	for i, path := range order {
		if err := os.WriteFile(path, []byte(updated[path]), 0644); err != nil {
			if i == 0 {
				return fmt.Sprintf("could not write file: %v", err), true
			}
			return fmt.Sprintf("could not write %s: %v — the %d file(s) before it were written",
				display(root, path), err, i), true
		}
	}

	if len(list) == 1 {
		return "OK, edited " + list[0].Path, false
	}
	return fmt.Sprintf("OK, %d edits across %d file(s): %s",
		len(list), len(order), strings.Join(displayAll(root, order), ", ")), false
}

// displayAll is the paths as the model gave them, for the line that says what
// was written.
func displayAll(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, display(root, p))
	}
	return out
}
