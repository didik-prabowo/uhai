// The contract for keeping conversations, and the rules that hold whatever
// backend keeps them to the same behaviour. Nothing here knows about files.
package session

import (
	"fmt"
	"path/filepath"
	"strings"
)

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

// LatestIn is the newest conversation held in one project, which is what
// -resume with no id should carry on: the newest anywhere is how a session
// about another tree gets resumed against this one, with the agent then
// working on files those messages never meant.
//
// Sessions with no root are not candidates. They were saved before the field
// existed and could belong to any project; guessing they belong to this one is
// the mistake this exists to stop.
func LatestIn(st Store, root string) (Session, error) {
	all, err := st.All()
	if err != nil {
		return Session{}, err
	}
	for _, s := range all {
		if s.Root != "" && SameRoot(s.Root, root) {
			return s, nil
		}
	}
	return Session{}, fmt.Errorf("no saved conversation for %s yet", root)
}

// SameRoot compares two project paths the way the daemon's socket does:
// symlinks resolved, so /tmp and /private/tmp are one project.
func SameRoot(a, b string) bool { return resolve(a) == resolve(b) }

func resolve(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Pick is the matching rule behind Load, kept apart from the storage so every
// backend answers Load the same way instead of inventing its own idea of what
// a prefix is. A store that reinvented it would be a bug nobody could see.
func Pick(all []Session, id string) (Session, error) {
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
