package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

// A session is one conversation, written to ~/.ouhai/sessions after every turn
// so a closed terminal — or a crash — does not take the work with it.
//
// ponytail: whole history rewritten each turn, one file per session. Fine for
// conversations that fit in a model's context; revisit if they ever do not.

// Session is what gets saved and what --resume brings back.
type Session struct {
	Started  time.Time          `json:"started"`
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
}

func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ouhai", "sessions"), nil
}

// Save writes the session, named after the moment it started so the newest one
// sorts last.
func (s Session) Save() error {
	if len(s.Messages) == 0 {
		return nil // nothing said yet
	}
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
	name := s.Started.Format("2006-01-02T15-04-05") + ".json"
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

// LatestSession returns the most recently started saved session.
func LatestSession() (Session, error) {
	var s Session
	dir, err := sessionsDir()
	if err != nil {
		return s, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s, fmt.Errorf("no saved sessions yet")
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return s, fmt.Errorf("no saved sessions yet")
	}
	sort.Strings(names) // the names are timestamps, so the last one is newest

	data, err := os.ReadFile(filepath.Join(dir, names[len(names)-1]))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s is corrupt: %w", names[len(names)-1], err)
	}
	return s, nil
}
