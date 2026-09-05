package gemini

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

// events writes chunks the way the API does with alt=sse: one JSON object per
// data line, each carrying whole parts rather than fragments of them.
func events(lines ...string) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("data: " + line + "\n\n")
	}
	return b.String()
}

func TestSendAssemblesTextAndFunctionCall(t *testing.T) {
	var got wireRequest
	var path string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path + "?" + r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)

		if key := r.Header.Get("X-Goog-Api-Key"); key != "k" {
			t.Errorf("the key travels in a header, not the query: %q", key)
		}

		io.WriteString(w, events(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"reading "}]}}],"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":0}}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"it now"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"main.go"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":34}}`,
		))
	}))
	defer server.Close()

	c, err := New(Options{Label: "gemini", BaseURL: server.URL, APIKey: "k", Model: "gemini-2.5-flash", MaxTokens: 8192})
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

	// The model is named in the path, and the stream has to be asked for as
	// events — without alt=sse the answer only parses once it is complete.
	if !strings.Contains(path, "/models/gemini-2.5-flash:streamGenerateContent") || !strings.Contains(path, "alt=sse") {
		t.Errorf("request went to %q", path)
	}
	if got.SystemInstruction == nil || got.SystemInstruction.Parts[0].Text != "be brief" {
		t.Errorf("the system prompt travels apart from the conversation: %+v", got.SystemInstruction)
	}
	if got.GenerationConfig.MaxOutputTokens != 8192 {
		t.Errorf("max tokens = %d", got.GenerationConfig.MaxOutputTokens)
	}
	// One tool object holding every function, not one object per function.
	if len(got.Tools) != 1 || len(got.Tools[0].FunctionDeclarations) != 1 {
		t.Errorf("tools went out wrong: %+v", got.Tools)
	}

	if streamed != "reading it now" {
		t.Fatalf("text must arrive as it is written, got %q", streamed)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("want a text block and a call, got %+v", resp.Content)
	}
	call := resp.Content[1]
	if call.ToolName != "read_file" || string(call.ToolInput) != `{"path":"main.go"}` {
		t.Fatalf("the call was not assembled: %+v", call)
	}
	if call.ToolUseID == "" {
		t.Fatal("the agent matches results to calls by id, so one has to be invented here")
	}
	// STOP alongside a call still means the turn is waiting on that call.
	if resp.StopReason != provider.StopToolUse {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if resp.Usage.Input != 1200 || resp.Usage.Output != 34 {
		t.Fatalf("token usage was not picked up: %+v", resp.Usage)
	}
}

// The assistant is called "model" here, and a tool result is matched to its
// call by name — so the id every other API hands out has to be looked back up
// while translating.
func TestToWireRenamesTheAssistantAndMatchesResultsByName(t *testing.T) {
	out := toWire([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "read it"}}},
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{{
			Type: provider.BlockToolUse, ToolUseID: "call_1", ToolName: "read_file",
			ToolInput: json.RawMessage(`{"path":"main.go"}`),
		}}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{
			Type: provider.BlockToolResult, ToolResultForID: "call_1", ToolResultText: "package main",
		}}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: provider.BlockText}}},
	})

	// The empty message is dropped: a content without parts is refused.
	if len(out) != 3 {
		t.Fatalf("want three contents, got %+v", out)
	}
	if out[1].Role != "model" {
		t.Errorf("the assistant is called %q here", out[1].Role)
	}
	result := out[2].Parts[0].FunctionResp
	if result == nil || result.Name != "read_file" {
		t.Fatalf("the result must name the tool it answers: %+v", out[2].Parts[0])
	}
	if result.Response["result"] != "package main" {
		t.Errorf("the result travels inside an object: %+v", result.Response)
	}
}

// A tool that printed nothing still has to say so, and a call with no
// arguments still has to send an object.
func TestEmptyResultAndEmptyArguments(t *testing.T) {
	out := toWire([]provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{{
			Type: provider.BlockToolUse, ToolUseID: "c1", ToolName: "glob",
		}}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{
			Type: provider.BlockToolResult, ToolResultForID: "c1",
		}}},
	})

	if got := string(out[0].Parts[0].FunctionCall.Args); got != "{}" {
		t.Errorf("missing arguments must read as {}, got %q", got)
	}
	if got := out[1].Parts[0].FunctionResp.Response["result"]; got != "(no output)" {
		t.Errorf("an empty result must still say something, got %q", got)
	}
}

func TestErrorsAreSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Retry-After keeps the test quick: a 429 without one now waits five
		// seconds and then ten, which is right in life and wrong in a test.
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"code":429,"message":"Quota exceeded for quota metric","status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer server.Close()

	c, _ := New(Options{Label: "gemini", BaseURL: server.URL, APIKey: "k", Model: "gemini-2.5-flash"})
	_, err := c.Send(context.Background(), provider.Request{})
	if err == nil || !strings.Contains(err.Error(), "Quota exceeded") {
		t.Fatalf("the server's reason must reach the user, got %v", err)
	}

	// An error mid-stream ends the turn rather than being swallowed.
	if _, err := parseStream(strings.NewReader(
		`data: {"error":{"message":"overloaded"}}`), nil); err == nil {
		t.Fatal("an error event must be surfaced")
	}
}

// Listing offers only what can hold a conversation: an embedding model in the
// picker is a puzzling failure two steps later. A deprecated model still
// answers, so it stays — the list is also what /model checks a typed name
// against, and dropping a working model there reports it as a typo.
func TestModelsOffersOnlyWhatCanChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[
			{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]},
			{"name":"models/gemini-1.0-pro","supportedGenerationMethods":["generateContent"],"deprecated":true}
		]}`)
	}))
	defer server.Close()

	c, _ := New(Options{Label: "gemini", BaseURL: server.URL, APIKey: "k", Model: "gemini-2.5-flash"})
	models, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "gemini-1.0-pro" || models[1] != "gemini-2.5-flash" {
		t.Fatalf("the embedding model must go and the deprecated one stay, got %v", models)
	}
}

// Gemini 3 stamps every tool call with a thought signature and answers 400 on
// the next turn if it does not come back: "Function call is missing a
// thought_signature in functionCall parts". Nothing else in uhai needs the
// token, so it is only ever read here and written back here — which is exactly
// how it came to be dropped.
func TestThoughtSignatureSurvivesTheRoundTrip(t *testing.T) {
	const sig = "EqoDCqcDARFNMg9qUlyUTubAJ231usCDYtyG6Uew"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, events(
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"glob","args":{"pattern":"*.go"}},"thoughtSignature":"`+sig+`"}]},"finishReason":"STOP"}]}`,
		))
	}))
	defer server.Close()

	c, err := New(Options{Label: "gemini", BaseURL: server.URL, APIKey: "k", Model: "gemini-3.5-flash", MaxTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Send(context.Background(), provider.Request{
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "cari file go"}},
		}},
		Tools: []provider.ToolSpec{{Name: "glob", JSONSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read off the wire and kept on the block, so it is stored with the
	// session and is still there when one is resumed.
	if got := resp.Content[0].Signature; got != sig {
		t.Fatalf("the signature was dropped on arrival: %q", got)
	}

	// And put back on the same part it arrived on.
	out := toWire([]provider.Message{{Role: provider.RoleAssistant, Content: resp.Content}})
	if got := out[0].Parts[0].ThoughtSignature; got != sig {
		t.Fatalf("the signature was not sent back: %q", got)
	}
	if out[0].Parts[0].FunctionCall == nil {
		t.Error("it belongs on the functionCall part, which is what the API checks")
	}
}

// The vendors that send no signature must not start sending an empty one:
// omitempty is what keeps the field off the wire for them.
func TestNoSignatureMeansNoField(t *testing.T) {
	out := toWire([]provider.Message{{
		Role: provider.RoleAssistant,
		Content: []provider.ContentBlock{{
			Type: provider.BlockToolUse, ToolUseID: "c1", ToolName: "glob",
			ToolInput: json.RawMessage(`{}`),
		}},
	}})
	body, err := json.Marshal(out[0].Parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "thoughtSignature") {
		t.Errorf("an absent signature must not be sent at all: %s", body)
	}
}
