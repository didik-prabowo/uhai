// The contract, checked from outside the package so it can reach both the
// implementation that ships and one written only to prove the contract is a
// contract. An external test package is what lets it import filestore without
// session importing it back.
package session_test

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
	"github.com/didik-prabowo/uhai/internal/session/filestore"
)

// memStore keeps conversations in a map and nothing on disk. It is here so the
// contract below has a second implementation to check against, which is what
// tells an interface from a shape: a session.Store written by reading Files would only
// ever reproduce Files.
type memStore struct{ byID map[string]session.Session }

func newMem() *memStore { return &memStore{byID: map[string]session.Session{}} }

func (m *memStore) Save(s session.Session) error {
	if len(s.Messages) == 0 {
		return nil
	}
	if s.ID == "" {
		s.ID = session.NewID()
	}
	s.Updated = time.Now()
	s.PID = os.Getpid()
	m.byID[s.ID] = s
	return nil
}

func (m *memStore) All() ([]session.Session, error) {
	if len(m.byID) == 0 {
		return nil, fmt.Errorf("no saved sessions yet")
	}
	out := make([]session.Session, 0, len(m.byID))
	for _, s := range m.byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (m *memStore) Load(id string) (session.Session, error) {
	all, err := m.All()
	if err != nil {
		return session.Session{}, err
	}
	return session.Pick(all, id)
}

// The contract every session.Store owes its callers, checked against each one there
// is. A backend added later is finished when this passes for it.
func TestStoreContract(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(t *testing.T) session.Store
	}{
		{"files", func(t *testing.T) session.Store { return filestore.New(t.TempDir()) }},
		{"memory", func(t *testing.T) session.Store { return newMem() }},
	} {
		t.Run(backend.name, func(t *testing.T) {
			st := backend.open(t)

			if _, err := session.Latest(st); err == nil {
				t.Fatal("with nothing kept, resuming must say so rather than hand back an empty session")
			}

			msg := func(text string) []provider.Message {
				return []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}}
			}
			first, second := session.New(), session.New()
			first.Model, first.Messages = "groq/a", msg("yang pertama")
			second.Model, second.Messages = "groq/b", msg("yang kedua")
			for _, s := range []session.Session{first, second} {
				if err := st.Save(s); err != nil {
					t.Fatal(err)
				}
			}

			// An empty conversation is not worth keeping anywhere.
			if err := st.Save(session.New()); err != nil {
				t.Fatal(err)
			}
			all, err := st.All()
			if err != nil || len(all) != 2 {
				t.Fatalf("two conversations were said, got %d: %v", len(all), err)
			}
			if all[0].ID != second.ID {
				t.Errorf("newest first: %+v", all)
			}

			got, err := session.Latest(st)
			if err != nil || got.ID != second.ID {
				t.Fatalf("latest is the one updated last: %+v %v", got, err)
			}
			if got, err := st.Load(first.ID); err != nil || got.Model != "groq/a" {
				t.Errorf("load by id: %+v %v", got, err)
			}
			if got, err := st.Load(first.ID[:4]); err != nil || got.Model != "groq/a" {
				t.Errorf("a prefix that names one is enough: %+v %v", got, err)
			}
			if _, err := st.Load("nope"); err == nil {
				t.Error("an id nobody kept must be reported")
			}
			if _, err := st.Load(""); err == nil {
				t.Error("the empty prefix matches everything, which names nothing")
			}

			// Saving again under the same id continues it rather than forking.
			second.Messages = append(second.Messages, msg("lagi")...)
			if err := st.Save(second); err != nil {
				t.Fatal(err)
			}
			if all, _ := st.All(); len(all) != 2 {
				t.Fatalf("resaving must not fork: %d", len(all))
			}
			if got, _ := st.Load(second.ID); len(got.Messages) != 2 {
				t.Errorf("the newer content must win: %d messages", len(got.Messages))
			}
		})
	}
}
