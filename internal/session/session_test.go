package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := Latest(); err == nil {
		t.Fatal("with nothing saved, resuming must say so rather than return an empty session")
	}

	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}
	}
	older := Session{Started: time.Now().Add(-time.Hour), Model: "groq/a", Messages: []provider.Message{msg("older")}}
	newer := Session{Started: time.Now(), Model: "groq/b", Messages: []provider.Message{msg("newer")}}
	for _, s := range []Session{older, newer} {
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}

	// An empty session is not worth a file.
	if err := (Session{Started: time.Now()}).Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Latest()
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
	t.Setenv("HOME", dir)

	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}
	}
	start := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	first := Session{Started: start, Model: "groq/a", Messages: []provider.Message{msg("first prompt\nsecond line")}}
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	second := Session{Started: start.Add(2 * time.Hour), Model: "groq/b", Messages: []provider.Message{msg("later")}}
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}

	// A session saved before ids existed: no id, no updated.
	old := filepath.Join(dir, ".uhai", "sessions", "2026-08-30T09-00-00.json")
	if err := os.WriteFile(old, []byte(`{"started":"2026-08-30T09:00:00Z","model":"groq/c","messages":[{"role":"user","content":[{"type":"text","text":"ancient"}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "2026-09-01T12-30-00" {
		t.Fatalf("sessions should come back newest first: %+v", all)
	}
	if all[2].ID != "2026-08-30T09-00-00" || all[2].Updated.IsZero() {
		t.Fatalf("a file written before ids existed must still be resumable: %+v", all[2])
	}

	got, err := Load("2026-09-01T10-30-00")
	if err != nil || got.Model != "groq/a" {
		t.Fatalf("load by id: %+v %v", got, err)
	}
	if got.Prompt() != "first prompt" {
		t.Errorf("the listing shows the first line asked, got %q", got.Prompt())
	}
	if _, err := Load("2026-09-01"); err == nil {
		t.Error("a prefix matching two sessions must ask for more of the id")
	}
	if got, err := Load("2026-08"); err != nil || got.Model != "groq/c" {
		t.Errorf("a prefix matching one session is enough: %+v %v", got, err)
	}
	if _, err := Load("nope"); err == nil {
		t.Error("an id nobody saved must be reported")
	}

	// Saving again under the same id continues that file rather than adding one.
	second.Messages = append(second.Messages, msg("more"))
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}
	if all, _ := All(); len(all) != 3 {
		t.Fatalf("resaving must not fork a file: %d sessions", len(all))
	}
}
