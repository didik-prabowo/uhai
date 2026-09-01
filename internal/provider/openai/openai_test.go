package openai

import (
	"encoding/json"
	"testing"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

func TestToWireHasilToolJadiPesanSendiri(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockText, Text: "list file"},
		}},
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockToolUse, ToolUseID: "c1", ToolName: "run_bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockToolResult, ToolResultForID: "c1", ToolResultText: "main.go"},
		}},
	}

	out := toWire("kamu asisten", msgs)
	if len(out) != 4 {
		t.Fatalf("harus system + user + assistant + tool = 4 pesan, dapat %d: %+v", len(out), out)
	}
	if out[0].Role != "system" {
		t.Fatalf("pesan pertama harus system: %+v", out[0])
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("tool call gak keterjemah: %+v", out[2])
	}
	// Hasil tool wajib role "tool" + tool_call_id, bukan nempel di pesan user.
	if out[3].Role != "tool" || out[3].ToolCallID != "c1" || out[3].Content != "main.go" {
		t.Fatalf("hasil tool salah bentuk: %+v", out[3])
	}
}

func TestParseToolCall(t *testing.T) {
	raw := []byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"cek dulu",
		"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]}}]}`)

	resp, err := parse(raw, 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != provider.StopToolUse {
		t.Fatalf("stop reason harus tool_use, dapat %q", resp.StopReason)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != provider.BlockText || resp.Content[1].ToolName != "read_file" {
		t.Fatalf("blok gak sesuai: %+v", resp.Content)
	}
}

func TestParseArgumenKosongJadiObjekKosong(t *testing.T) {
	// Sebagian model kirim arguments "" buat tool tanpa argumen — kalau
	// diteruskan apa adanya, json.Unmarshal di sisi tool bakal error.
	raw := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"c1","function":{"name":"x","arguments":""}}]}}]}`)
	resp, err := parse(raw, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Content[0].ToolInput); got != "{}" {
		t.Fatalf("arguments kosong harus jadi {}, dapat %q", got)
	}
}

func TestParseError(t *testing.T) {
	raw := []byte(`{"error":{"message":"Invalid API Key","type":"invalid_request_error"}}`)
	if _, err := parse(raw, 401); err == nil {
		t.Fatal("error dari provider harus diteruskan")
	}

	if _, err := parse([]byte("<html>502</html>"), 502); err == nil {
		t.Fatal("respons non-JSON harus jadi error, bukan panic")
	}
}
