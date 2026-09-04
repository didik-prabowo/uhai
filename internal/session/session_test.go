package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
)

func TestSessionRoundTrip(t *testing.T) {
	st := Files{Dir: t.TempDir()}

	if _, err := Latest(st); err == nil {
		t.Fatal("with nothing saved, resuming must say so rather than return an empty session")
	}

	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}
	}
	older := Session{Started: time.Now().Add(-time.Hour), Model: "groq/a", Messages: []provider.Message{msg("older")}}
	newer := Session{Started: time.Now(), Model: "groq/b", Messages: []provider.Message{msg("newer")}}
	for _, s := range []Session{older, newer} {
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	// An empty session is not worth a file.
	if err := st.Save(Session{Started: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := Latest(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "groq/b" || len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "newer" {
		t.Fatalf("the newest session should come back, got %+v", got)
	}
}

// An id names a session, an old file without one still resumes, and part of an
// id is enough as long as it names only one.
func TestSessionsAreFoundByID(t *testing.T) {
	dir := t.TempDir()
	st := Files{Dir: dir}

	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}
	}
	start := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	first := New()
	first.Started, first.Model, first.Messages = start, "groq/a", []provider.Message{msg("first prompt\nsecond line")}
	if err := st.Save(first); err != nil {
		t.Fatal(err)
	}
	second := New()
	second.Started, second.Model, second.Messages = start.Add(2*time.Hour), "groq/b", []provider.Message{msg("later")}
	if err := st.Save(second); err != nil {
		t.Fatal(err)
	}

	// A session saved before ids existed: no id, no updated.
	old := filepath.Join(dir, "2026-08-30T09-00-00.json")
	if err := os.WriteFile(old, []byte(`{"started":"2026-08-30T09:00:00Z","model":"groq/c","messages":[{"role":"user","content":[{"type":"text","text":"ancient"}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := st.All()
	if err != nil {
		t.Fatal(err)
	}
	// Newest first now comes from what the files say, not from what they are
	// called: an opaque name sorts alphabetically, which is no order at all.
	if len(all) != 3 || all[0].ID != second.ID || all[0].Model != "groq/b" {
		t.Fatalf("sessions should come back newest first: %+v", all)
	}
	if all[2].ID != "2026-08-30T09-00-00" || all[2].Updated.IsZero() {
		t.Fatalf("a file written before ids existed must still be resumable: %+v", all[2])
	}
	if all[0].PID != os.Getpid() {
		t.Errorf("the pid that wrote last is the one recorded, got %d", all[0].PID)
	}

	got, err := st.Load(first.ID)
	if err != nil || got.Model != "groq/a" {
		t.Fatalf("load by id: %+v %v", got, err)
	}
	if got.Prompt() != "first prompt" {
		t.Errorf("the listing shows the first line asked, got %q", got.Prompt())
	}
	if got, err := st.Load(first.ID[:4]); err != nil || got.Model != "groq/a" {
		t.Errorf("four characters is enough to name one: %+v %v", got, err)
	}
	if got, err := st.Load("2026-08"); err != nil || got.Model != "groq/c" {
		t.Errorf("a prefix matching one session is enough: %+v %v", got, err)
	}
	if _, err := st.Load("nope"); err == nil {
		t.Error("an id nobody saved must be reported")
	}

	// The id says nothing about when or where, on purpose: the date it used to
	// carry was only good to the second, so two conversations begun in the
	// same second were one file and the second to save replaced the first.
	if first.ID == second.ID {
		t.Error("two conversations must never share an id")
	}
	if strings.Contains(first.ID, "2026") {
		t.Errorf("an id must not encode the clock, got %q", first.ID)
	}
	if len(first.ID) != 13 {
		t.Errorf("an id is 8 random bytes in base32, got %q", first.ID)
	}

	// Saving again under the same id continues that file rather than adding one.
	second.Messages = append(second.Messages, msg("more"))
	if err := st.Save(second); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.All(); len(all) != 3 {
		t.Fatalf("resaving must not fork a file: %d sessions", len(all))
	}
}

// memStore keeps conversations in a map and nothing on disk. It is here so the
// contract below has a second implementation to check against, which is what
// tells an interface from a shape: a Store written by reading Files would only
// ever reproduce Files.
type memStore struct{ byID map[string]Session }

func newMem() *memStore { return &memStore{byID: map[string]Session{}} }

func (m *memStore) Save(s Session) error {
	if len(s.Messages) == 0 {
		return nil
	}
	if s.ID == "" {
		s.ID = newID()
	}
	s.Updated = time.Now()
	s.PID = os.Getpid()
	m.byID[s.ID] = s
	return nil
}

func (m *memStore) All() ([]Session, error) {
	if len(m.byID) == 0 {
		return nil, fmt.Errorf("no saved sessions yet")
	}
	out := make([]Session, 0, len(m.byID))
	for _, s := range m.byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (m *memStore) Load(id string) (Session, error) {
	all, err := m.All()
	if err != nil {
		return Session{}, err
	}
	return pick(all, id)
}

// The contract every Store owes its callers, checked against each one there
// is. A backend added later is finished when this passes for it.
func TestStoreContract(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(t *testing.T) Store
	}{
		{"files", func(t *testing.T) Store { return Files{Dir: t.TempDir()} }},
		{"memory", func(t *testing.T) Store { return newMem() }},
	} {
		t.Run(backend.name, func(t *testing.T) {
			st := backend.open(t)

			if _, err := Latest(st); err == nil {
				t.Fatal("with nothing kept, resuming must say so rather than hand back an empty session")
			}

			msg := func(text string) []provider.Message {
				return []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}}
			}
			first, second := New(), New()
			first.Model, first.Messages = "groq/a", msg("yang pertama")
			second.Model, second.Messages = "groq/b", msg("yang kedua")
			for _, s := range []Session{first, second} {
				if err := st.Save(s); err != nil {
					t.Fatal(err)
				}
			}

			// An empty conversation is not worth keeping anywhere.
			if err := st.Save(New()); err != nil {
				t.Fatal(err)
			}
			all, err := st.All()
			if err != nil || len(all) != 2 {
				t.Fatalf("two conversations were said, got %d: %v", len(all), err)
			}
			if all[0].ID != second.ID {
				t.Errorf("newest first: %+v", all)
			}

			got, err := Latest(st)
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
