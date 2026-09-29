// Package filestore keeps conversations as one JSON file each, under
// ~/.uhai/sessions. It is the store uhai ships, and the only place that knows
// a conversation is ever a file: session holds the contract, this holds the
// answer to it, and the import only ever points this way.
//
// ponytail: whole history rewritten each turn, one file per session. Fine for
// conversations that fit in a model's context; revisit if they ever do not.
package filestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/session"
)

// store is unexported because nothing outside needs the type — New hands back
// the contract, and a caller that named the concrete type would be back where
// it started.
type store struct{ dir string }

// New keeps conversations under dir. An empty dir means ~/.uhai/sessions; it
// is a parameter so a test can point somewhere of its own without moving HOME.
func New(dir string) session.Store { return store{dir: dir} }

func (f store) root() (string, error) {
	if f.dir != "" {
		return f.dir, nil
	}
	return config.InDir("sessions")
}

// Save writes the conversation under its id.
func (f store) Save(s session.Session) error {
	if len(s.Messages) == 0 {
		return nil // nothing said yet
	}
	if s.ID == "" {
		s.ID = session.NewID() // a session.Session built as a literal rather than by New
	}
	s.Updated = time.Now()
	// Whoever wrote last is who holds it: a resumed conversation is held by
	// the process resuming it, not by the one that started it months ago.
	s.PID = os.Getpid()
	dir, err := f.root()
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

	// Written beside the file and renamed onto it, not into it. os.WriteFile
	// truncates first, so a crash, a full disk or a kill between the truncate
	// and the last byte left the conversation not stale but gone — and this is
	// the file the daemon reads back when it picks a project up again, so the
	// loss survives the restart that would otherwise hide it. A rename is
	// atomic on both systems uhai runs on: a reader sees the previous save or
	// this one, never half of either.
	//
	// The temporary name ends in .tmp rather than .json so that All ignores
	// one left behind by a kill; named .json it would be listed as a
	// conversation and resumed from, which is the corrupt file this was
	// supposed to stop existing.
	//
	// ponytail: not fsync'd, so a power cut can still lose the last save even
	// though no file is corrupt. The failure this closes is the truncate
	// window, which is the one that happened; fsync on every turn is a disk
	// flush in the way of the next prompt. Add it if a conversation is ever
	// lost to a machine going down rather than to a process dying.
	tmp, err := os.CreateTemp(dir, s.ID+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing to remove once the rename has taken it

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// CreateTemp makes it 0600, and a session file has always been 0644.
	// Changing that is a decision of its own and not this one's to make.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, s.ID+".json"))
}

// All lists what has been saved, newest first.
//
// ponytail: every file is read whole to list them, messages included. Fine for
// a directory of conversations; sort out a header if it ever is not.
func (f store) All() ([]session.Session, error) {
	dir, err := f.root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no saved sessions yet")
	}

	var out []session.Session
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
func readSession(dir, name string) (session.Session, error) {
	var s session.Session
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
func (f store) Load(id string) (session.Session, error) {
	all, err := f.All()
	if err != nil {
		return session.Session{}, err
	}
	return session.Pick(all, id)
}
