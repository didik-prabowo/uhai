package orchestrator

import (
	"testing"
	"time"

	"github.com/didik-prabowo/ouhai/internal/agent"
	"github.com/didik-prabowo/ouhai/internal/config"
	"github.com/didik-prabowo/ouhai/internal/provider"
)

// A conversation is resumed with the model it was held with. Carrying on with
// whatever settings.json says today would change the context window and the
// price of every turn without saying so.
func TestResumeBringsBackTheModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "k")

	saved := config.Session{
		Started: time.Now(),
		Model:   "groq/openai/gpt-oss-120b",
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "where were we"}},
		}},
	}
	if err := saved.Save(); err != nil {
		t.Fatal(err)
	}

	a := agent.New(nil)
	note, err := restore(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("nothing should have been lost: %s", note)
	}
	if len(a.History) != 1 {
		t.Fatalf("history did not come back: %+v", a.History)
	}
	if a.Provider == nil || a.Provider.Name() != saved.Model {
		t.Fatalf("resumed on the wrong model: %v", a.Provider)
	}
	if a.MaxContextTokens == 0 {
		t.Error("the window follows the model, so it has to be set with it")
	}

	// A model that can no longer be built is a line, not a silent swap.
	saved.Model = "nosuchprovider/x"
	if err := saved.Save(); err != nil {
		t.Fatal(err)
	}
	b := agent.New(nil)
	note, err = restore(b, "")
	if err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Error("failing to restore the model must be said out loud")
	}
}

// An id picks which conversation comes back, not just the newest.
func TestResumeByID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	msg := provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "older"}}}
	older := config.Session{Started: time.Now().Add(-2 * time.Hour), Messages: []provider.Message{msg}}
	newer := config.Session{Started: time.Now(), Messages: []provider.Message{msg}}
	for _, s := range []config.Session{older, newer} {
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}

	a := agent.New(nil)
	if _, err := restore(a, older.Started.Format("2006-01-02T15-04-05")); err != nil {
		t.Fatal(err)
	}
	if _, err := restore(a, "2029-01-01T00-00-00"); err == nil {
		t.Error("an id nobody saved must be an error, not an empty start")
	}
}
