package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

// A session is one conversation, written to ~/.ouhai/sessions after every turn
// so a closed terminal — or a crash — does not take the work with it.
//
// ponytail: whole history rewritten each turn, one file per session. Fine for
// conversations that fit in a model's context; revisit if they ever do not.

// Session is what gets saved and what -resume brings back.
type Session struct {
	// ID names the file and is what -resume takes. It is the moment the
	// conversation started, which sorts and reads as a date at the same time.
	ID       string             `json:"id"`
	Started  time.Time          `json:"started"`
	Updated  time.Time          `json:"updated"` // moves every turn, so a listing shows what was worked on last
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
}

// idLayout is the shape of an id: a timestamp with nothing in it a filesystem
// dislikes, so the id and the file name are the same string.
const idLayout = "2006-01-02T15-04-05"

func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ouhai", "sessions"), nil
}

// Save writes the session under its id, which sorts as a date, so the newest
// one sorts last.
func (s Session) Save() error {
	if len(s.Messages) == 0 {
		return nil // nothing said yet
	}
	s.ID = s.Identity()
	s.Updated = time.Now()
	dir, err := sessionsDir()
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

// Identity is the id this session has or will have once it is saved. One
// place decides what an id looks like, so nothing has to spell the layout out
// a second time.
func (s Session) Identity() string {
	if s.ID != "" {
		return s.ID
	}
	return s.Started.Format(idLayout)
}

// Sessions lists what has been saved, newest first.
//
// ponytail: every file is read whole to list them, messages included. Fine for
// a directory of conversations; sort out a header if it ever is not.
func Sessions() ([]Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no saved sessions yet")
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // the names are timestamps

	var out []Session
	for _, name := range names {
		s, err := readSession(dir, name)
		if err != nil {
			continue // one corrupt file must not hide the rest
		}
		out = append(out, s)
	}
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

// LatestSession returns the most recently saved session.
func LatestSession() (Session, error) {
	all, err := Sessions()
	if err != nil {
		return Session{}, err
	}
	return all[0], nil
}

// LoadSession finds one by id, or by any prefix of an id that names only one —
// typing the date is usually enough. The id is matched against what was found
// rather than pasted into a path, so it cannot be used to read elsewhere.
func LoadSession(id string) (Session, error) {
	all, err := Sessions()
	if err != nil {
		return Session{}, err
	}

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
