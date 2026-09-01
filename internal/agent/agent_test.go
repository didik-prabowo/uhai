package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

// fakeProvider minta write_file di panggilan pertama, lalu selesai.
type fakeProvider struct {
	path  string
	calls int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Send(req provider.Request) (*provider.Response, error) {
	f.calls++
	if f.calls == 1 {
		input, _ := json.Marshal(map[string]string{"path": f.path, "content": "halo"})
		return &provider.Response{
			StopReason: provider.StopToolUse,
			Content: []provider.ContentBlock{{
				Type:      provider.BlockToolUse,
				ToolUseID: "t1",
				ToolName:  "write_file",
				ToolInput: input,
			}},
		}, nil
	}
	return &provider.Response{
		StopReason: provider.StopEndTurn,
		Content:    []provider.ContentBlock{{Type: provider.BlockText, Text: "oke"}},
	}, nil
}

func TestConfirmDeniedTidakEksekusiTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	a := New(&fakeProvider{path: path})
	a.Confirm = func(string, string) bool { return false }

	if err := a.Ask("tulis file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file kebuat padahal user nolak: %v", err)
	}

	last := a.History[len(a.History)-2].Content[0]
	if !last.ToolResultError || !strings.Contains(last.ToolResultText, "menolak") {
		t.Fatalf("model gak dikasih tahu penolakan: %+v", last)
	}
}

func TestConfirmDefaultNolak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	a := New(&fakeProvider{path: path}) // Confirm bawaan New(), gak di-override
	if err := a.Ask("tulis file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Confirm default harusnya nolak, tapi file kebuat")
	}
}

func TestConfirmDiizinkanEksekusiTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	a := New(&fakeProvider{path: path})
	a.Confirm = func(string, string) bool { return true }

	if err := a.Ask("tulis file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file harusnya kebuat: %v", err)
	}
}
