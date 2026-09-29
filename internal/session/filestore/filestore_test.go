package filestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
)

func TestSessionRoundTrip(t *testing.T) {
	st := New(t.TempDir())

	if _, err := session.Latest(st); err == nil {
		t.Fatal("with nothing saved, resuming must say so rather than return an empty session")
	}

	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}
	}
	older := session.Session{Started: time.Now().Add(-time.Hour), Model: "openai/a", Messages: []provider.Message{msg("older")}}
	newer := session.Session{Started: time.Now(), Model: "openai/b", Messages: []provider.Message{msg("newer")}}
	for _, s := range []session.Session{older, newer} {
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
	}

	// An empty session is not worth a file.
	if err := st.Save(session.Session{Started: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := session.Latest(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/b" || len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "newer" {
		t.Fatalf("the newest session should come back, got %+v", got)
	}
}

// An id names a session, an old file without one still resumes, and part of an
// id is enough as long as it names only one.
func TestSessionsAreFoundByID(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)

	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: text}}}
	}
	start := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	first := session.New()
	first.Started, first.Model, first.Messages = start, "openai/a", []provider.Message{msg("first prompt\nsecond line")}
	if err := st.Save(first); err != nil {
		t.Fatal(err)
	}
	second := session.New()
	second.Started, second.Model, second.Messages = start.Add(2*time.Hour), "openai/b", []provider.Message{msg("later")}
	if err := st.Save(second); err != nil {
		t.Fatal(err)
	}

	// A session saved before ids existed: no id, no updated.
	old := filepath.Join(dir, "2026-08-30T09-00-00.json")
	if err := os.WriteFile(old, []byte(`{"started":"2026-08-30T09:00:00Z","model":"openai/c","messages":[{"role":"user","content":[{"type":"text","text":"ancient"}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := st.All()
	if err != nil {
		t.Fatal(err)
	}
	// Newest first now comes from what the files say, not from what they are
	// called: an opaque name sorts alphabetically, which is no order at all.
	if len(all) != 3 || all[0].ID != second.ID || all[0].Model != "openai/b" {
		t.Fatalf("sessions should come back newest first: %+v", all)
	}
	if all[2].ID != "2026-08-30T09-00-00" || all[2].Updated.IsZero() {
		t.Fatalf("a file written before ids existed must still be resumable: %+v", all[2])
	}
	if all[0].PID != os.Getpid() {
		t.Errorf("the pid that wrote last is the one recorded, got %d", all[0].PID)
	}

	got, err := st.Load(first.ID)
	if err != nil || got.Model != "openai/a" {
		t.Fatalf("load by id: %+v %v", got, err)
	}
	if got.Prompt() != "first prompt" {
		t.Errorf("the listing shows the first line asked, got %q", got.Prompt())
	}
	if got, err := st.Load(first.ID[:4]); err != nil || got.Model != "openai/a" {
		t.Errorf("four characters is enough to name one: %+v %v", got, err)
	}
	if got, err := st.Load("2026-08"); err != nil || got.Model != "openai/c" {
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

// A saved conversation is provider.Message and provider.ContentBlock written
// straight to disk, which quietly made those two the file format. This pins
// the names: rename a field in provider — a change that touches no storage
// code and looks entirely safe — and this fails, instead of every saved
// conversation coming back with its tool calls empty and nothing saying so.
func TestSavedSessionKeepsItsWireNames(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)

	s := session.New()
	s.Model = "openai/a"
	s.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockText, Text: "jalankan testnya"},
		}},
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockToolUse, ToolUseID: "call_1", ToolName: "run_bash",
				ToolInput: []byte(`{"command":"go test ./..."}`)},
		}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockToolResult, ToolResultForID: "call_1",
				ToolResultText: "ok", ToolResultError: false},
		}},
	}
	if err := st.Save(s); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, s.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"Role"`, `"Content"`, `"Type"`, `"Text"`,
		`"ToolUseID"`, `"ToolName"`, `"ToolInput"`,
		`"ToolResultForID"`, `"ToolResultText"`, `"ToolResultError"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("the file format lost %s — a rename in provider changed what is on disk:\n%s", key, raw)
		}
	}

	// The half that matters: a file written before this test existed still
	// comes back whole, tool call and all.
	back, err := st.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Messages) != 3 {
		t.Fatalf("three messages went in, %d came back", len(back.Messages))
	}
	call := back.Messages[1].Content[0]
	if call.ToolUseID != "call_1" || call.ToolName != "run_bash" || string(call.ToolInput) != `{"command":"go test ./..."}` {
		t.Errorf("the tool call did not survive the round trip: %+v", call)
	}
	if back.Messages[2].Content[0].ToolResultForID != "call_1" {
		t.Errorf("the result lost the call it answers: %+v", back.Messages[2].Content[0])
	}
}

// A save replaces the file by renaming onto it, so nothing ever reads a
// half-written conversation. The observable half of that is the temporary
// name: it must not look like a session, or a file left behind by a kill is
// listed as one and resumed from.
func TestSavingLeavesOneFileAndNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)

	s := session.New()
	s.Model = "openai/a"
	s.Messages = []provider.Message{{
		Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "sekali"}},
	}}
	if err := st.Save(s); err != nil {
		t.Fatal(err)
	}
	s.Messages = append(s.Messages, provider.Message{
		Role: provider.RoleAssistant, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "dua kali"}},
	})
	if err := st.Save(s); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != s.ID+".json" {
		t.Fatalf("two saves of one conversation are one file: %v", names)
	}

	all, err := st.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || len(all[0].Messages) != 2 {
		t.Fatalf("the newer save is what is on disk: %+v", all)
	}

	// And what a kill between the write and the rename leaves behind is not a
	// conversation. Written by hand because a test cannot be killed halfway:
	// this is the shape of the file, which is the part the listing has to
	// ignore.
	if err := os.WriteFile(filepath.Join(dir, s.ID+".2411063.tmp"), []byte(`{"messages":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	if all, err := st.All(); err != nil || len(all) != 1 {
		t.Fatalf("a half-written file must not be listed as a session: %+v %v", all, err)
	}
}
