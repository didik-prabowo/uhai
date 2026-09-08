package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/didik-prabowo/uhai/internal/provider"
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
	resp, err := parseStream(strings.NewReader(sse), func(d string) { streamed += d }, nil)
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
	if _, err := parseStream(strings.NewReader(`data: {"error":{"message":"rate limited"}}`), nil, nil); err == nil {
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

// A thinking model sends its working out apart from the answer, under a name
// that depends on the vendor. It reaches its own hook and never the answer's:
// mixed in, the thinking would be spoken as if it were the reply.
func TestReasoningIsStreamedApartFromTheAnswer(t *testing.T) {
	sse := `data: {"choices":[{"delta":{"reasoning_content":"the file is"}}]}

data: {"choices":[{"delta":{"reasoning":" probably main.go"}}]}

data: {"choices":[{"delta":{"content":"Reading main.go"}}]}

data: [DONE]
`
	var answer, thinking string
	resp, err := parseStream(strings.NewReader(sse),
		func(d string) { answer += d },
		func(d string) { thinking += d })
	if err != nil {
		t.Fatal(err)
	}
	if thinking != "the file is probably main.go" {
		t.Errorf("both spellings are the same thing, got %q", thinking)
	}
	if answer != "Reading main.go" {
		t.Errorf("the answer must not carry the thinking, got %q", answer)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "Reading main.go" {
		t.Errorf("the response keeps the answer only: %+v", resp.Content)
	}
}

// A gateway that fills in both names sends the same text twice. Added
// together it wrote every chunk on top of itself, so "</think>" reached the
// screen as "</</thinkthink>>".
func TestBothSpellingsInOneChunkAreOneThought(t *testing.T) {
	sse := `data: {"choices":[{"delta":{"reasoning_content":"</","reasoning":"</"}}]}

data: {"choices":[{"delta":{"reasoning_content":"think","reasoning":"think"}}]}

data: {"choices":[{"delta":{"content":"sudah"}}]}

data: [DONE]
`
	var thinking string
	if _, err := parseStream(strings.NewReader(sse), nil, func(d string) { thinking += d }); err != nil {
		t.Fatal(err)
	}
	if thinking != "</think" {
		t.Errorf("the same text under two names is one thought, got %q", thinking)
	}
}

// Every OpenAI-style vendor caches without being asked and reports it, and
// uhai read none of it: two identical requests to Z.ai came back with 3 cached
// tokens and then 1395 of 1397, all of it priced as fresh input.
//
// prompt_tokens already includes the cached ones, unlike Anthropic where they
// arrive beside each other, so the split has to be taken back out or the same
// tokens are billed twice.
func TestCachedPromptTokensAreSplitOutOfInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"halo"}}],"usage":{"prompt_tokens":1397,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":1395}}}`+"\n\ndata: [DONE]\n")
	}))
	defer server.Close()

	c, err := New(Options{Label: "zai", BaseURL: server.URL, APIKey: "k", Model: "glm-4.7-flash"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Send(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CacheRead != 1395 {
		t.Errorf("cached tokens = %d, want 1395", resp.Usage.CacheRead)
	}
	// 1397 total, 1395 of them cached: two were fresh.
	if resp.Usage.Input != 2 {
		t.Errorf("fresh input = %d, want 2 — cached tokens must not be billed twice", resp.Usage.Input)
	}
	if resp.Usage.Input+resp.Usage.CacheRead != 1397 {
		t.Errorf("the split has to add back up to prompt_tokens, got %d", resp.Usage.Input+resp.Usage.CacheRead)
	}
}

// A vendor that reports no details at all is the old behaviour: everything
// fresh, nothing cached, no negative numbers.
func TestNoCacheDetailsMeansEverythingIsFresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"halo"}}],"usage":{"prompt_tokens":900,"completion_tokens":4}}`+"\n\ndata: [DONE]\n")
	}))
	defer server.Close()

	c, _ := New(Options{Label: "openai", BaseURL: server.URL, APIKey: "k", Model: "gpt-4o-mini"})
	resp, err := c.Send(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.Input != 900 || resp.Usage.CacheRead != 0 {
		t.Errorf("want 900 fresh and 0 cached, got %d and %d", resp.Usage.Input, resp.Usage.CacheRead)
	}
}

// The thought signature Gemini 3 refuses to continue without arrives as
// extra_content on the tool call and has to go back out on the same call next
// turn. It used to be the gemini client's job, and the reason that client
// existed; it is opaque here, which is why one RawMessage covers whichever
// vendor does this next.
func TestToolCallExtraContentSurvivesTheRoundTrip(t *testing.T) {
	const sig = `{"google":{"thought_signature":"Ev4DCvsDARFNMg8XLYUH"}}`

	// In: what the endpoint sent becomes the block's Signature.
	var in wireMessage
	if err := json.Unmarshal([]byte(`{
		"role": "assistant",
		"tool_calls": [{"id": "call_1", "type": "function",
			"function": {"name": "ls", "arguments": "{\"path\":\".\"}"},
			"extra_content": `+sig+`}]
	}`), &in); err != nil {
		t.Fatal(err)
	}
	blocks, _ := toBlocks(in)
	var use provider.ContentBlock
	for _, b := range blocks {
		if b.Type == provider.BlockToolUse {
			use = b
		}
	}
	if use.ToolUseID != "call_1" {
		t.Fatalf("no tool call came back: %+v", blocks)
	}
	if use.Signature == "" {
		t.Fatal("the signature was dropped on the way in — the next turn is a 400")
	}

	// Out: and goes back on the wire untouched.
	out := toWire("", []provider.Message{{Role: provider.RoleAssistant, Content: blocks}})
	var found string
	for _, m := range out {
		for _, c := range m.ToolCalls {
			found = string(c.Extra)
		}
	}
	if found == "" {
		t.Fatal("the signature was dropped on the way out")
	}
	var want, got any
	if err := json.Unmarshal([]byte(sig), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(found), &got); err != nil {
		t.Fatalf("what went out is not the JSON that came in: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("signature changed in transit:\n want %v\n got  %v", want, got)
	}
}

// A call with nothing extra must not grow an empty field: most endpoints have
// never heard of extra_content and some validate what they are sent.
func TestToolCallWithoutExtraContentSendsNone(t *testing.T) {
	out := toWire("", []provider.Message{{Role: provider.RoleAssistant, Content: []provider.ContentBlock{{
		Type: provider.BlockToolUse, ToolUseID: "c1", ToolName: "ls", ToolInput: []byte(`{}`),
	}}}})
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "extra_content") {
		t.Errorf("an empty extra_content reached the wire: %s", body)
	}
}

// Google's compat endpoint is the only one that namespaces its ids, and left
// on, "models/gemini-3.5-flash" would miss the model table.
func TestModelNamespaceIsTrimmed(t *testing.T) {
	if got := trimNamespace("models/gemini-3.5-flash"); got != "gemini-3.5-flash" {
		t.Errorf("got %q", got)
	}
	if got := trimNamespace("glm-4.7"); got != "glm-4.7" {
		t.Errorf("an ordinary id was rewritten to %q", got)
	}
}

// Newest first, which the sort computes and a sort.Strings after it used to
// undo — the ordering was thrown away one line after being worked out. It hid
// because a gateway reports no dates at all, so the tie-break made it look
// alphabetical either way.
func TestModelsAreNewestFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[
			{"id":"aaa-old",  "created": 100},
			{"id":"zzz-new",  "created": 900},
			{"id":"mmm-mid",  "created": 500},
			{"id":"bbb-tie",  "created": 900}
		]}`)
	}))
	defer srv.Close()

	c, err := New(Options{Label: "t", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	// Same date falls back to the id, so the two 900s are in name order.
	want := []string{"bbb-tie", "zzz-new", "mmm-mid", "aaa-old"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An endpoint that reports no dates is left in id order, which is the only
// order there is to give it.
func TestModelsWithoutDatesSortByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"cx/gpt-6"},{"id":"cc/claude-5"},{"id":"cx/gpt-5"}]}`)
	}))
	defer srv.Close()

	c, err := New(Options{Label: "t", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cc/claude-5", "cx/gpt-5", "cx/gpt-6"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
