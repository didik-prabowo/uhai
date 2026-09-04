// Package session is the conversation and where it is kept: Session is the
// data, Store is the contract for keeping it, and Files is the one
// implementation uhai ships — a JSON file per conversation under
// ~/.uhai/sessions, written after every turn and read back by -resume.
//
// It lives apart from config because the two are opposites. Settings, keys and
// permissions are input a person writes to change what uhai does; a session is
// output uhai produces by running. Nothing in config ever needed a Session —
// it only shared the directory.
package session

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
)

// A session is one conversation, written to ~/.uhai/sessions after every turn
// so a closed terminal — or a crash — does not take the work with it.
//
// ponytail: whole history rewritten each turn, one file per session. Fine for
// conversations that fit in a model's context; revisit if they ever do not.

// Session is what gets saved and what -resume brings back.
type Session struct {
	// ID names the file and is what -resume takes. It carries no meaning on
	// purpose: a name that encodes when or where a thing was made is a fact
	// duplicated in two places, and the copy in the name is the one that goes
	// stale. When and what are fields, and the listing prints them.
	ID       string             `json:"id"`
	Started  time.Time          `json:"started"`
	Updated  time.Time          `json:"updated"` // moves every turn, so a listing shows what was worked on last
	PID      int                `json:"pid"`     // the process that held it, for telling two live sessions apart
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
}

// Store is where conversations are kept. Files is the one uhai ships; the
// contract is here so a second — sqlite, postgres, something over a network —
// can take its place without a caller knowing which it got. The composition
// root picks one at startup and nothing downstream chooses again.
//
// Three methods, because that is what the front ends ask for. Latest is a
// package function rather than a method: every backend can answer it from All,
// and one that could do better (ORDER BY updated DESC LIMIT 1) should grow an
// optional interface for it the way provider.ModelLister does, rather than
// making every implementation carry a method most of them would fake.
type Store interface {
	// Save writes the conversation under its id, replacing what was there.
	Save(Session) error

	// All lists what has been kept, newest first.
	All() ([]Session, error)

	// Load finds one by id, or by any prefix of an id that names only one.
	Load(id string) (Session, error)
}

// Latest is the most recently updated conversation, which is what -resume
// takes when no id is given.
func Latest(st Store) (Session, error) {
	all, err := st.All()
	if err != nil {
		return Session{}, err
	}
	return all[0], nil
}

// idBytes is how much randomness an id carries. Eight bytes is 64 bits: a
// directory of conversations will not collide, and base32 turns it into 13
// characters of which the first four are already enough to name one.
const idBytes = 8

// New starts a conversation with an id of its own, minted here rather than at
// save time so the id is the same string before and after the first turn — the
// prompt prints it on the way out, and it has to be the one on disk.
func New() Session {
	return Session{ID: newID(), Started: time.Now(), PID: os.Getpid()}
}

// newID is opaque and says nothing, which is the point. It is lower case and
// has no padding so it survives being read aloud, typed by hand, and pasted
// into a shell without quoting.
func newID() string {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; if it ever does, a clashing
		// id would overwrite a conversation, so fall back to something that
		// cannot repeat within a process rather than to the empty string.
		return strconv.FormatInt(time.Now().UnixNano(), 32)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

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
// backend answers Load the same way instead of inventing its own idea of what
// a prefix is. Export it when a second backend lives outside this package.
func pick(all []Session, id string) (Session, error) {
	var found []Session
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
		if strings.HasPrefix(s.ID, id) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return Session{}, fmt.Errorf("no saved session with id %q", id)
	case 1:
		return found[0], nil
	default:
		return Session{}, fmt.Errorf("%q matches %d sessions — use more of the id", id, len(found))
	}
}

// Prompt is the first thing that was asked, for telling one session from
// another in a listing.
func (s Session) Prompt() string {
	for _, m := range s.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, block := range m.Content {
			if block.Type == provider.BlockText && strings.TrimSpace(block.Text) != "" {
				return strings.TrimSpace(strings.SplitN(block.Text, "\n", 2)[0])
			}
		}
	}
	return ""
}
