package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

func TestToWireToolResultBecomesOwnMessage(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockText, Text: "list files"},
		}},
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockToolUse, ToolUseID: "c1", ToolName: "run_bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			{Type: provider.BlockToolResult, ToolResultForID: "c1", ToolResultText: "main.go"},
		}},
	}

	out := toWire("you are an assistant", msgs)
	if len(out) != 4 {
		t.Fatalf("expected system + user + assistant + tool = 4 messages, got %d: %+v", len(out), out)
	}
	if out[0].Role != "system" {
		t.Fatalf("the first message must be system: %+v", out[0])
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("tool call was not translated: %+v", out[2])
	}
	// A tool result needs role "tool" plus tool_call_id, not to ride along with the user message.
	if out[3].Role != "tool" || out[3].ToolCallID != "c1" || out[3].Content != "main.go" {
		t.Fatalf("tool result has the wrong shape: %+v", out[3])
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
		t.Fatalf("stop reason should be tool_use, got %q", resp.StopReason)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != provider.BlockText || resp.Content[1].ToolName != "read_file" {
		t.Fatalf("unexpected blocks: %+v", resp.Content)
	}
}

func TestParseEmptyArgumentsBecomeEmptyObject(t *testing.T) {
	// Some models send arguments "" for tools without arguments — passing that
	// through as-is makes json.Unmarshal fail on the tool side.
	raw := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"c1","function":{"name":"x","arguments":""}}]}}]}`)
	resp, err := parse(raw, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Content[0].ToolInput); got != "{}" {
		t.Fatalf("empty arguments should become {}, got %q", got)
	}
}

func TestParseError(t *testing.T) {
	raw := []byte(`{"error":{"message":"Invalid API Key","type":"invalid_request_error"}}`)
	if _, err := parse(raw, 401); err == nil {
		t.Fatal("a provider error must be surfaced")
	}

	if _, err := parse([]byte("<html>502</html>"), 502); err == nil {
		t.Fatal("a non-JSON response must be an error, not a panic")
	}
}

func TestParseStreamAssemblesTextAndToolCall(t *testing.T) {
	// Text arrives in pieces, and so does the tool call: name first, then the
	// arguments JSON split across chunks.
	sse := `data: {"choices":[{"delta":{"content":"cek "}}]}

data: {"choices":[{"delta":{"content":"dulu"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}

data: {"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":34}}

data: [DONE]
`
	var streamed string
	resp, err := parseStream(strings.NewReader(sse), func(d string) { streamed += d })
	if err != nil {
		t.Fatal(err)
	}
	if streamed != "cek dulu" {
		t.Fatalf("deltas were not reported live: %q", streamed)
	}
	if len(resp.Content) != 2 || resp.Content[0].Text != "cek dulu" {
		t.Fatalf("text was not assembled: %+v", resp.Content)
	}
	call := resp.Content[1]
	if call.ToolUseID != "c1" || call.ToolName != "read_file" || string(call.ToolInput) != `{"path":"main.go"}` {
		t.Fatalf("tool call was not assembled: %+v", call)
	}
	if resp.StopReason != provider.StopToolUse {
		t.Fatalf("stop reason should be tool_use, got %q", resp.StopReason)
	}
	// The usage chunk carries no choices and must still be read.
	if resp.Usage.Input != 1200 || resp.Usage.Output != 34 {
		t.Fatalf("token usage was not picked up: %+v", resp.Usage)
	}
}

func TestParseStreamError(t *testing.T) {
	if _, err := parseStream(strings.NewReader(`data: {"error":{"message":"rate limited"}}`), nil); err == nil {
		t.Fatal("an error chunk must be surfaced")
	}
}

// A server that does not know stream_options answers 400; the client should
// drop the field and ask again rather than lose the turn.
func TestSendRetriesWithoutStreamOptions(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))

		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"unknown field: stream_options"}}`)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()

	c, err := New(Options{Label: "test", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var streamed string
	resp, err := c.Send(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "hi"}}}},
		Stream:   func(d string) { streamed += d },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected one retry, got %d requests", len(bodies))
	}
	if !strings.Contains(bodies[0], "stream_options") {
		t.Fatalf("the first attempt should ask for usage: %s", bodies[0])
	}
	if strings.Contains(bodies[1], "stream_options") {
		t.Fatalf("the retry should drop the field: %s", bodies[1])
	}
	if streamed != "hi" || resp.Content[0].Text != "hi" {
		t.Fatalf("the answer did not survive the retry: %q / %+v", streamed, resp.Content)
	}
}

// A 400 about anything else is a real error and must not be retried.
func TestSendDoesNotRetryOtherBadRequests(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model \"m\" does not exist"}}`)
	}))
	defer srv.Close()

	c, _ := New(Options{Label: "test", BaseURL: srv.URL, Model: "m"})
	_, err := c.Send(context.Background(), provider.Request{})
	if err == nil {
		t.Fatal("a bad model name must be reported")
	}
	if calls != 1 {
		t.Fatalf("that request should not be retried, made %d calls", calls)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("the server's message should reach the user: %v", err)
	}
}

// A tool that printed nothing — touch, mkdir, an empty file — still has to
// send a content field. Dropping it is a 400: "for 'role:tool' the following
// must be satisfied: property 'content' is missing".
func TestEmptyToolResultStillHasContent(t *testing.T) {
	out := toWire("", []provider.Message{{
		Role: provider.RoleUser,
		Content: []provider.ContentBlock{{
			Type:            provider.BlockToolResult,
			ToolResultForID: "call_1",
		}},
	}})

	if len(out) != 1 || out[0].Role != "tool" {
		t.Fatalf("want one tool message, got %+v", out)
	}
	if out[0].Content == "" {
		t.Fatal("content must not be empty, the API refuses it")
	}

	// And it must survive being marshalled, which is where omitempty bites.
	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content"`) {
		t.Fatalf("content was dropped on the way to JSON: %s", raw)
	}
}
