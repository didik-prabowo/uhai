package config

import (
	"testing"
	"time"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := LatestSession(); err == nil {
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

	got, err := LatestSession()
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "groq/b" || len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "newer" {
		t.Fatalf("the newest session should come back, got %+v", got)
	}
}
