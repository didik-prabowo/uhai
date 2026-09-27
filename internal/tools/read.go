// Reading a file. The one tool the model reaches for most, and one of the
// three that never has to ask.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// readBudget is how much of a file one read returns. Below maxResultLen so the
// line that says where to carry on cannot be the thing Execute cuts off, which
// would leave the model knowing there is more and not how to reach it.
const readBudget = maxResultLen - 512

// readTool is the read_file tool: what the model is told about it, and the
// thing that runs.
type readTool struct{}

func (readTool) Name() string       { return NameRead }
func (readTool) NeedsConfirm() bool { return false }
func (readTool) Description() string {
	return "Read a file from disk as text. A long file comes back in pieces: the result says which lines it gave you and the offset to ask for next."
}
func (readTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Relative or absolute path to the file"},
				"offset": {"type": "integer", "description": "First line to return, counting from 1. Default 1"},
				"limit": {"type": "integer", "description": "How many lines to return. Default is as many as fit"}
			},
			"required": ["path"]
		}`)
}

// Run returns a window of the file rather than the whole of it.
//
// It used to read the file into memory whole and hand back every byte, which
// Execute then cut to 8,000 characters from the front — so a file larger than
// that could not be read past its first pages by any means the model had.
// `internal/cli/cli_test.go` is 107,890 bytes: the model could reach 7% of it,
// always the same 7%, with nothing saying how to see the rest. An empty result
// and a truncated one both read as "this is the file".
//
// Reading line by line rather than whole also gives it a memory ceiling, which
// it never had: os.ReadFile on a 500 MB log is 500 MB, and in the daemon the
// process that runs out of memory is holding every project's conversation.
func (readTool) Run(_ context.Context, root string, input json.RawMessage) (string, bool) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}

	f, err := os.Open(resolve(root, args.Path))
	if err != nil {
		return fmt.Sprintf("could not read file: %v", err), true
	}
	defer f.Close()

	from := max(args.Offset, 1)

	// bufio.Reader and not a Scanner: a Scanner stops at a maximum token size
	// and reports it through an error that has to be asked for, which is how
	// run_bash lost a whole command's output to one long line.
	r := bufio.NewReader(f)
	var out strings.Builder
	var line, kept int
	var more bool

	for !more {
		text, err := r.ReadString('\n')
		if text != "" {
			line++
			switch {
			case line < from:
				// Before the window. Counted, not kept.
			case args.Limit > 0 && kept >= args.Limit:
				more = true
			case out.Len()+len(text) > readBudget:
				// A single line past the whole budget would otherwise return
				// nothing, and an empty result makes the same offset the only
				// thing to try again — a loop the model cannot see out of.
				if kept == 0 {
					out.WriteString(strings.ToValidUTF8(text[:readBudget], "") + "…[line cut]\n")
					kept++
				}
				more = true
			default:
				out.WriteString(text)
				kept++
			}
		}
		if err != nil {
			break
		}
	}

	switch {
	case line == 0:
		return "the file is empty", false
	case kept == 0:
		// Said with the count, because the useful next move is a smaller offset
		// and the model cannot guess one from a refusal.
		return fmt.Sprintf("the file has %d lines; offset %d is past the end", line, from), true
	case more:
		return out.String() + fmt.Sprintf("\n…[lines %d-%d, more follow — read on with offset %d]",
			from, from+kept-1, from+kept), false
	case from > 1:
		// Which lines these were, since the model asked for a window and the
		// text alone does not say where it sat.
		return out.String() + fmt.Sprintf("\n…[lines %d-%d, the end of the file]", from, from+kept-1), false
	}
	return out.String(), false
}
