package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

// events writes one server-sent event per line pair, the way the Messages API
// does: an "event:" line naming it, then the "data:" line carrying it.
func events(lines ...string) string {
	var b strings.Builder
	for _, line := range lines {
		var e struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(line), &e)
		b.WriteString("event: " + e.Type + "\ndata: " + line + "\n\n")
	}
	return b.String()
}

func TestSendAssemblesTextAndToolCall(t *testing.T) {
	var got wireRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		if key := r.Header.Get("X-Api-Key"); key != "k" {
			t.Errorf("the key must travel in X-Api-Key, got %q", key)
		}
		if v := r.Header.Get("Anthropic-Version"); v != apiVersion {
			t.Errorf("the API version must be pinned, got %q", v)
		}

		io.WriteString(w, events(
			`{"type":"message_start","message":{"usage":{"input_tokens":1200,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reading "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"it now"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read_file"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":34}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer server.Close()

	c, err := New(Options{Label: "anthropic", BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}

	var streamed string
	resp, err := c.Send(context.Background(), provider.Request{
		System: "be brief",
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "read main.go"}},
		}},
		Tools:  []provider.ToolSpec{{Name: "read_file", JSONSchema: json.RawMessage(`{"type":"object"}`)}},
		Stream: func(delta string) { streamed += delta },
	})
	if err != nil {
		t.Fatal(err)
	}

	// The Messages API refuses a request without a ceiling on the answer.
	if got.MaxTokens != 8192 || got.System != "be brief" || !got.Stream {
		t.Fatalf("the request went out wrong: %+v", got)
	}

	if streamed != "reading it now" {
		t.Fatalf("text must arrive as it is written, got %q", streamed)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("want a text block and a tool call, got %+v", resp.Content)
	}
	call := resp.Content[1]
	// A tool call's arguments arrive as fragments of JSON and are only valid
	// once the block closes.
	if call.ToolName != "read_file" || call.ToolUseID != "tu_1" || string(call.ToolInput) != `{"path":"main.go"}` {
		t.Fatalf("the tool call was not assembled: %+v", call)
	}
	if resp.StopReason != provider.StopToolUse {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if resp.Usage.Input != 1200 || resp.Usage.Output != 34 {
		t.Fatalf("token usage was not picked up: %+v", resp.Usage)
	}
}

// A turn that only calls a tool opens with a text block that never receives a
// delta; it must not reach the history as an empty answer.
func TestEmptyTextBlockIsDropped(t *testing.T) {
	resp, err := parseStream(strings.NewReader(events(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"glob"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
	)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != provider.BlockToolUse {
		t.Fatalf("want the tool call alone, got %+v", resp.Content)
	}
	// A tool taking no arguments sends no JSON at all, which is not the same
	// as sending nothing valid.
	if string(resp.Content[0].ToolInput) != "{}" {
		t.Fatalf("missing arguments must read as {}, got %q", resp.Content[0].ToolInput)
	}
}

func TestToWireCarriesToolResults(t *testing.T) {
	out := toWire([]provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{{
			Type: provider.BlockToolUse, ToolUseID: "tu_1", ToolName: "read_file",
		}}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{
			Type: provider.BlockToolResult, ToolResultForID: "tu_1", ToolResultText: "boom", ToolResultError: true,
		}}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText}}},
	})

	// The empty message is dropped: the API refuses one, and it says nothing.
	if len(out) != 2 {
		t.Fatalf("want two messages, got %+v", out)
	}
	// A tool call with no arguments still needs an object on the wire.
	if string(out[0].Content[0].Input) != "{}" {
		t.Fatalf("tool call input = %q", out[0].Content[0].Input)
	}
	result := out[1].Content[0]
	if result.Type != "tool_result" || result.ToolUseID != "tu_1" || !result.IsError {
		t.Fatalf("the tool result was not carried over: %+v", result)
	}
}

func TestErrorsAreSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"max_tokens is required"}}`)
	}))
	defer server.Close()

	c, _ := New(Options{Label: "anthropic", BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5"})
	_, err := c.Send(context.Background(), provider.Request{})
	if err == nil || !strings.Contains(err.Error(), "max_tokens is required") {
		t.Fatalf("the server's reason must reach the user, got %v", err)
	}

	// An error mid-stream ends the turn rather than being swallowed.
	if _, err := parseStream(strings.NewReader(
		`data: {"type":"error","error":{"message":"overloaded"}}`), nil); err == nil {
		t.Fatal("an error event must be surfaced")
	}
}

// The same on this side: a tool_result block without content is refused.
func TestEmptyToolResultStillHasContent(t *testing.T) {
	out := toWire([]provider.Message{{
		Role: provider.RoleUser,
		Content: []provider.ContentBlock{{
			Type:            provider.BlockToolResult,
			ToolResultForID: "tu_1",
		}},
	}})

	if len(out) != 1 || len(out[0].Content) != 1 {
		t.Fatalf("want one message with one block, got %+v", out)
	}
	raw, err := json.Marshal(out[0].Content[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content"`) {
		t.Fatalf("content was dropped on the way to JSON: %s", raw)
	}
}

// A key linked to an identity can act in several workspaces, so the API
// refuses to guess: without this header it answers 400. An ordinary key must
// not have one sent, since it carries its workspace itself.
func TestWorkspaceIDTravelsAsAHeader(t *testing.T) {
	for _, id := range []string{"wrkspc_1", ""} {
		var got string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Anthropic-Workspace-Id")
			io.WriteString(w, events(
				`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			))
		}))

		c, err := New(Options{Label: "anthropic", BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5", WorkspaceID: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Send(context.Background(), provider.Request{}); err != nil {
			t.Fatal(err)
		}
		server.Close()

		if got != id {
			t.Errorf("workspace header = %q, want %q", got, id)
		}
	}
}
