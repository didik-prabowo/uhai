// Reading and changing files. Every failure here comes back as text the model
// can act on rather than as a silent empty result.
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return fmt.Sprintf("could not read file: %v", err), true
	}
	return string(data), false
}

func writeFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if dir := filepath.Dir(args.Path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Sprintf("could not create folder: %v", err), true
		}
	}
	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		return fmt.Sprintf("could not write file: %v", err), true
	}
	return fmt.Sprintf("OK, wrote %d bytes to %s", len(args.Content), args.Path), false
}

func editFile(input json.RawMessage) (string, bool) {
	var args struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if args.Old == "" {
		return "old must not be empty — use write_file to create a file", true
	}

	data, err := os.ReadFile(args.Path)
	if err != nil {
		return fmt.Sprintf("could not read file: %v", err), true
	}

	// An ambiguous match is refused rather than guessed: replacing the wrong
	// one of several matches silently corrupts the file.
	switch n := strings.Count(string(data), args.Old); n {
	case 0:
		return "the old text is not in the file — read it again, it may have changed", true
	case 1:
	default:
		return fmt.Sprintf("the old text appears %d times — include surrounding lines to make it unique", n), true
	}

	updated := strings.Replace(string(data), args.Old, args.New, 1)
	if err := os.WriteFile(args.Path, []byte(updated), 0644); err != nil {
		return fmt.Sprintf("could not write file: %v", err), true
	}
	return fmt.Sprintf("OK, edited %s", args.Path), false
}
