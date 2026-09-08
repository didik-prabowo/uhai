package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/didik-prabowo/uhai/internal/provider"
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
			`{"type":"message_start","message":{"usage":{"input_tokens":1200,"output_tokens":0,"cache_read_input_tokens":9000,"cache_creation_input_tokens":40}}}`,
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
	if got.MaxTokens != 8192 || !got.Stream {
		t.Fatalf("the request went out wrong: %+v", got)
	}

	// The system prompt goes as a block carrying a cache breakpoint, not as a
	// string. Tools render before it, so this one mark covers every byte of a
	// request that does not change within a turn — around nine thousand tokens
	// here, resent on every iteration of the tool loop.
	if len(got.System) != 1 || got.System[0].Text != "be brief" {
		t.Fatalf("the system prompt went out wrong: %+v", got.System)
	}
	if got.System[0].Cache == nil || got.System[0].Cache.Type != "ephemeral" {
		t.Fatalf("the prefix must be marked cacheable: %+v", got.System[0])
	}

	// The cache tally is reported once, when the message opens, and never
	// again — and it has to be kept apart from Input, since reading a cached
	// prefix is billed at a fraction and writing one at a premium.
	if resp.Usage.CacheRead != 9000 || resp.Usage.CacheWrite != 40 || resp.Usage.Input != 1200 {
		t.Fatalf("the cache tally must survive the stream: %+v", resp.Usage)
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
	)), nil, nil)
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
		`data: {"type":"error","error":{"message":"overloaded"}}`), nil, nil); err == nil {
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

// Thinking arrives as its own block, signed, and has to survive being stored
// and sent back. Reasoning used to be shown and let go, which is right for a
// vendor that streams the text and wants nothing back and wrong here: the API
// checks the signature against the content, so a block dropped from the
// history — or tidied on the way out — is refused on the next turn, and the
// error names a rule rather than the turn that broke it.
func TestThinkingIsKeptSignedAndSentBack(t *testing.T) {
	var reasoning strings.Builder
	resp, err := parseStream(strings.NewReader(events(
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the file is read "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"before it is written"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"jawaban"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	)), nil, func(d string) { reasoning.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}

	// Shown while it arrives, so a model thinking for twenty seconds does not
	// look like one that has hung.
	if got := reasoning.String(); got != "the file is read before it is written" {
		t.Fatalf("the working out has to reach the caller as it arrives, got %q", got)
	}

	if len(resp.Content) != 2 || resp.Content[0].Type != provider.BlockThinking {
		t.Fatalf("the thinking block has to be kept, got %+v", resp.Content)
	}
	if resp.Content[0].Signature != "sig123" {
		t.Fatalf("a thinking block without its signature is refused on the next turn, got %q",
			resp.Content[0].Signature)
	}

	// Back out unchanged, in the same order it came.
	back := toWire([]provider.Message{{Role: provider.RoleAssistant, Content: resp.Content}})
	if len(back) != 1 || len(back[0].Content) != 2 {
		t.Fatalf("both blocks have to go back, got %+v", back)
	}
	if b := back[0].Content[0]; b.Type != "thinking" ||
		b.Thinking != "the file is read before it is written" || b.Signature != "sig123" {
		t.Fatalf("the block has to go back exactly as it came, got %+v", b)
	}
}

// A model asked not to show its working still sends the block, with no text in
// it. That one has to go back too: it is empty, not absent, and the rule is
// about what was edited rather than what was read.
func TestAnEmptyThinkingBlockStillGoesBack(t *testing.T) {
	blocks := []provider.ContentBlock{
		{Type: provider.BlockThinking, Text: "", Signature: "sig456"},
		{Type: provider.BlockText, Text: "jawaban"},
	}
	back := toWire([]provider.Message{{Role: provider.RoleAssistant, Content: blocks}})
	if len(back) != 1 || len(back[0].Content) != 2 {
		t.Fatalf("an empty thinking block is not an empty text block, got %+v", back)
	}
	if b := back[0].Content[0]; b.Type != "thinking" || b.Signature != "sig456" {
		t.Fatalf("the signature is the half that has to survive, got %+v", b)
	}
}

// The parameters go out only for the models that take them. budget_tokens is
// not an older spelling of adaptive thinking — the 5 generation answers 400 to
// it — so a model that predates the parameter is sent neither, and effort
// never travels alone.
func TestThinkingAndEffortAreSentOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name       string
		thinking   bool
		effort     string
		wantThink  bool
		wantEffort string
	}{
		{name: "off by default"},
		{name: "adaptive with effort", thinking: true, effort: "xhigh", wantThink: true, wantEffort: "xhigh"},
		{name: "adaptive without effort", thinking: true, wantThink: true},
		{name: "effort alone is not sent", effort: "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got wireRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				json.Unmarshal(body, &got)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, events(
					`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				))
			}))
			defer server.Close()

			c, err := New(Options{Label: "anthropic", BaseURL: server.URL, APIKey: "k",
				Model: "claude-opus-5", Thinking: tc.thinking, Effort: tc.effort})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Send(context.Background(), provider.Request{
				Messages: []provider.Message{{Role: provider.RoleUser,
					Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}}}},
			}); err != nil {
				t.Fatal(err)
			}

			if (got.Thinking != nil) != tc.wantThink {
				t.Fatalf("thinking sent = %v, want %v", got.Thinking != nil, tc.wantThink)
			}
			if tc.wantThink {
				if got.Thinking.Type != "adaptive" {
					t.Fatalf("adaptive is the only shape the current models take, got %q", got.Thinking.Type)
				}
				// Without this the blocks arrive empty and the screen shows a
				// pause where the working out should be.
				if got.Thinking.Display != "summarized" {
					t.Fatalf("the working out has to be asked for, got display %q", got.Thinking.Display)
				}
			}
			effort := ""
			if got.OutputConfig != nil {
				effort = got.OutputConfig.Effort
			}
			if effort != tc.wantEffort {
				t.Fatalf("effort sent = %q, want %q", effort, tc.wantEffort)
			}
		})
	}
}
