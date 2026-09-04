// Files is the store uhai ships: one JSON file per conversation under
// ~/.uhai/sessions, rewritten after every turn so a closed terminal — or a
// crash — does not take the work with it.
//
// ponytail: whole history rewritten each turn, one file per session. Fine for
// conversations that fit in a model's context; revisit if they ever do not.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Files keeps one JSON file per conversation. A zero Files uses
// ~/.uhai/sessions; Dir is there so a test can point at a directory of its own
// without moving HOME.
type Files struct{ Dir string }

var _ Store = Files{}

func (f Files) dir() (string, error) {
	if f.Dir != "" {
		return f.Dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".uhai", "sessions"), nil
}

// Save writes the conversation under its id.
func (f Files) Save(s Session) error {
	if len(s.Messages) == 0 {
		return nil // nothing said yet
	}
	if s.ID == "" {
		s.ID = newID() // a Session built as a literal rather than by New
	}
	s.Updated = time.Now()
	// Whoever wrote last is who holds it: a resumed conversation is held by
	// the process resuming it, not by the one that started it months ago.
	s.PID = os.Getpid()
	dir, err := f.dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.ID+".json"), data, 0o644)
}

// All lists what has been saved, newest first.
//
// ponytail: every file is read whole to list them, messages included. Fine for
// a directory of conversations; sort out a header if it ever is not.
func (f Files) All() ([]Session, error) {
	dir, err := f.dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no saved sessions yet")
	}

	var out []Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		s, err := readSession(dir, e.Name())
		if err != nil {
			continue // one corrupt file must not hide the rest
		}
		out = append(out, s)
	}
	// Sorted on what the sessions say rather than on what they are called.
	// The names used to be timestamps and sorting them was the same thing;
	// now the name says nothing, and every file is read here anyway.
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if len(out) == 0 {
		return nil, fmt.Errorf("no saved sessions yet")
	}
	return out, nil
}

// readSession fills in what sessions saved before ids existed do not carry, so
// an old file resumes like any other.
func readSession(dir, name string) (Session, error) {
	var s Session
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s is corrupt: %w", name, err)
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(name, ".json")
	}
	if s.Updated.IsZero() {
		s.Updated = s.Started
	}
	return s, nil
}

// Load finds one by id, or by any prefix of an id that names only one — four
// characters normally do. The id is matched against what was found rather than
// pasted into a path, so it cannot be used to read a file elsewhere.
func (f Files) Load(id string) (Session, error) {
	all, err := f.All()
	if err != nil {
		return Session{}, err
	}
	return pick(all, id)
}

// pick is the matching rule itself, kept apart from the storage so every
