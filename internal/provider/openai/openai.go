// Package openai is the client for every provider speaking OpenAI-style Chat
// Completions: OpenAI itself, Gemini through its compat endpoint, and any
// gateway or local server added with /connect. Only the base URL, key and
// model name differ, so one implementation covers them all — which is also why
// adding an endpoint needs no code, only two settings.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
)

// headerTimeout bounds the wait for the first byte only. A whole-request
// timeout would cut off long answers, which streaming makes normal — the user
// interrupts instead.
//
// Three minutes rather than one: a 35,000-token diff sent to a busy free tier
// took longer than a minute to answer at all, and "timeout awaiting response
// headers" is a lie about what happened. A dead connection now hangs longer
// before it is called dead, which esc and /stop already answer.
const headerTimeout = 3 * time.Minute

// listTimeout bounds listing models. That request is not a stream and nothing
// watches the keyboard while it runs, so it needs a limit of its own.
const listTimeout = 15 * time.Second

// Options builds a client. BaseURL includes the version, e.g.
// "https://api.z.ai/api/paas/v4".
type Options struct {
	Label   string // shown to the user, e.g. "openai"
	BaseURL string
	APIKey  string // may be empty for local servers such as Ollama
	Model   string
}

type Client struct {
	opts Options
	http *http.Client
}

func New(o Options) (*Client, error) {
	if o.BaseURL == "" {
		return nil, fmt.Errorf("empty baseURL")
	}
	if o.Model == "" {
		return nil, fmt.Errorf("empty model")
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	return &Client{opts: o, http: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: headerTimeout}}}, nil
}

func (c *Client) Name() string { return c.opts.Label + "/" + c.opts.Model }

// --- OpenAI wire format ---

type wireMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
		// Arguments is a JSON string, not an object — that is the OpenAI shape,
		// not a typo.
		Arguments string `json:"arguments"`
	} `json:"function"`

	// Extra is whatever the vendor hung off the call that the schema has no
	// field for, kept opaque and sent back untouched. Google's compat endpoint
	// puts a thought signature here, and Gemini 3 answers 400 on the next turn
	// without it — so this is not decoration, it is what makes a second tool
	// call possible at all. Nothing here reads inside it, which is why one
	// json.RawMessage covers whichever vendor does this next.
	Extra json.RawMessage `json:"extra_content,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`

	// ReasoningEffort is how hard to think, this format's spelling of what
	// Anthropic calls output_config.effort. Omitted unless asked for: the
	// models that do not reason refuse it, and the field travels only once
	// something has said which model this actually is.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// StreamOptions asks for a final chunk carrying the token counts, which a
	// stream otherwise leaves out. It is a pointer so it can be dropped for
	// servers that do not know the field.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// wireChunk is one server-sent event of a streamed completion.
type wireChunk struct {
	// Model is who answered. Every chunk carries it, and behind a gateway it
	// is the only place the answer to that question exists: "plan-deep" is an
	// alias over five models, and the chunks name the one that took the turn.
	Model string `json:"model"`

	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Thinking models put their working out here, apart from the
			// answer. GLM and DeepSeek call it reasoning_content; others say
			// reasoning. Whichever arrives is the same thing.
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
				Extra json.RawMessage `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`

		// PromptDetails carries what the vendor served from its own cache.
		// Unlike Anthropic, which reports cached tokens beside the fresh
		// ones, an OpenAI-style prompt_tokens already includes them — so it
		// is a split of one number, not a second number, and Input has to
		// have it taken back out or the same tokens are billed twice.
		//
		// Every OpenAI-style vendor here does this without being asked: two
		// identical requests to Z.ai reported 3 cached tokens and then 1395
		// of 1397. uhai read neither, and priced the whole prefix as fresh
		// input on every turn.
		PromptDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// toWire translates neutral messages into the OpenAI shape. One neutral
// Message may become several wire messages: each tool result needs its own.
func toWire(system string, msgs []provider.Message) []wireMessage {
	out := []wireMessage{}
	if system != "" {
		out = append(out, wireMessage{Role: "system", Content: system})
	}

	for _, m := range msgs {
		var text strings.Builder
		var calls []wireCall

		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockText:
				text.WriteString(b.Text)
			case provider.BlockToolUse:
				var call wireCall
				call.ID, call.Type = b.ToolUseID, "function"
				call.Function.Name = b.ToolName
				call.Function.Arguments = string(b.ToolInput)
				// Back out exactly as it came in. Whatever it means is the
				// vendor's business; ours is not to drop it.
				call.Extra = json.RawMessage(b.Signature)
				calls = append(calls, call)
			case provider.BlockToolResult:
				// A tool result becomes its own message with role "tool".
				// Content must be there even when the tool printed nothing —
				// omitempty would drop it, and the API refuses a tool message
				// without one. Saying so is also better than saying nothing:
				// the model can tell success from silence.
				out = append(out, wireMessage{
					Role:       "tool",
					ToolCallID: b.ToolResultForID,
					Content:    orNoOutput(b.ToolResultText),
				})
			}
		}

		if text.Len() > 0 || len(calls) > 0 {
			out = append(out, wireMessage{
				Role:      string(m.Role),
				Content:   text.String(),
				ToolCalls: calls,
			})
		}
	}
	return out
}

// orNoOutput keeps an empty result from vanishing on the wire.
func orNoOutput(text string) string {
	if strings.TrimSpace(text) == "" {
		return "(no output)"
	}
	return text
}

func toWireTools(specs []provider.ToolSpec) []wireTool {
	var out []wireTool
	for _, s := range specs {
		var t wireTool
		t.Type = "function"
		t.Function.Name = s.Name
		t.Function.Description = s.Description
		t.Function.Parameters = s.JSONSchema
		out = append(out, t)
	}
	return out
}

// trimNamespace drops the "models/" Google's OpenAI-compatible endpoint puts
// in front of every id. Nothing else does it, and left on it would make the
// setting read "gemini/models/gemini-3.5-flash" — which works on the wire and
// misses the model table, since that matches on the last segment.
func trimNamespace(id string) string { return strings.TrimPrefix(id, "models/") }

// Models lists the model ids the endpoint offers (GET /models). Every
// OpenAI-compatible server implements it, so /model works for Gemini,
// Ollama and the rest without vendor-specific code.
func (c *Client) Models() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if c.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var body struct {
		Data []struct {
			ID               string   `json:"id"`
			Created          int64    `json:"created"`
			Active           *bool    `json:"active"`
			InputModalities  []string `json:"input_modalities"`
			OutputModalities []string `json:"output_modalities"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("model list is not JSON (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if body.Error != nil {
		return nil, fmt.Errorf("provider error (HTTP %d): %s", resp.StatusCode, body.Error.Message)
	}

	type chatModel struct {
		id      string
		created int64
	}
	models := make([]chatModel, 0, len(body.Data))
	for _, m := range body.Data {
		if m.Active != nil && !*m.Active {
			continue
		}
		// Only what cannot hold a conversation is dropped, because this list
		// is also what /model checks a typed name against: a model filtered
		// out here is reported as one the provider never heard of. Lacking
		// tools is not such a reason — uhai runs those with UseTools off —
		// and neither is a small window, which only compacts sooner.
		//
		// Older OpenAI-compatible endpoints may omit modality metadata, so
		// keep those models and only filter when the fields are present.
		if len(m.InputModalities) > 0 && (!slices.Contains(m.InputModalities, "text") || !slices.Contains(m.OutputModalities, "text")) {
			continue
		}
		if strings.Contains(strings.ToLower(m.ID), "prompt-guard") {
			continue
		}
		models = append(models, chatModel{id: trimNamespace(m.ID), created: m.Created})
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].created == models[j].created {
			return models[i].id < models[j].id
		}
		return models[i].created > models[j].created
	})
	// No sort.Strings here, and that is the point: one used to follow this
	// loop and undo the sort above it entirely, so the newest-first order was
	// computed and then thrown away. An endpoint that reports no dates — a
	// gateway usually does not — falls to id ascending through the tie-break,
	// which is what it looked like all along and hid the mistake.
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("the endpoint returned no models")
	}
	return ids, nil
}

func (c *Client) Send(ctx context.Context, req provider.Request) (*provider.Response, error) {
	wire := wireRequest{
		Model:         c.opts.Model,
		Messages:      toWire(req.System, req.Messages),
		Tools:         toWireTools(req.Tools),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},

		// Only when something has said which model this is. Behind an alias
		// the first turn cannot know, so it goes without and the answer names
		// the model for every turn after it.
		ReasoningEffort: req.Effort,
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}

	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Not every OpenAI-compatible server knows stream_options. Losing the
	// token counts is a fair trade; losing the answer is not, so ask again
	// without it.
	if resp.StatusCode == http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(raw), "stream_options") {
			return parse(raw, resp.StatusCode)
		}
		wire.StreamOptions = nil
		if body, err = json.Marshal(wire); err != nil {
			return nil, err
		}
		if resp, err = c.post(ctx, body); err != nil {
			return nil, err
		}
		defer resp.Body.Close()
	}

	// Errors do not come back as a stream: read the whole body and let parse
	// produce the message.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(resp.Body)
		return parse(raw, resp.StatusCode)
	}
	return parseStream(resp.Body, req.Stream, req.Reasoning)
}

// post sends the request. The body is kept as bytes so every attempt can send
// it again; the retrying itself is the same for every vendor and lives in the
// provider package.
func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	return provider.Post(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.opts.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
		}
		return req, nil
	})
}

// parseStream reads a server-sent-event stream of chat completion chunks,
// reporting text as it arrives and assembling everything into one response.
// Tool calls arrive in pieces too: the first chunk carries id and name, later
// ones append fragments of the argument JSON, keyed by index.
func parseStream(body io.Reader, onDelta, onReasoning func(string)) (*provider.Response, error) {
	var text strings.Builder
	var usage provider.Usage
	var model string
	calls := map[int]*wireCall{}
	var order []int

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // one chunk can be long
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue // blank lines and SSE comments
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var chunk wireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // a chunk we cannot read is not worth killing the answer for
		}
		if model == "" {
			model = chunk.Model // every chunk repeats it; the first is enough
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("provider error: %s", chunk.Error.Message)
		}
		// The usage chunk carries no choices, so it is read before that check.
		if chunk.Usage != nil {
			cached := chunk.Usage.PromptDetails.CachedTokens
			fresh := chunk.Usage.PromptTokens - cached
			if fresh < 0 {
				fresh = 0 // a vendor that reports more cached than prompt
			}
			usage = provider.Usage{Input: fresh, CacheRead: cached, Output: chunk.Usage.CompletionTokens}
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta
		// Whichever of the two names arrived, not both: a gateway that fills
		// in the other name as well was sending the same text twice, and
		// adding them wrote every chunk on top of itself — "</think>" arrived
		// as "</</thinkthink>>", which is not a tag any more and was drawn as
		// it stood.
		thought := delta.ReasoningContent
		if thought == "" {
			thought = delta.Reasoning
		}
		if thought != "" && onReasoning != nil {
			onReasoning(thought)
		}
		if delta.Content != "" {
			text.WriteString(delta.Content)
			if onDelta != nil {
				onDelta(delta.Content)
			}
		}
		for _, tc := range delta.ToolCalls {
			call := calls[tc.Index]
			if call == nil {
				call = &wireCall{}
				calls[tc.Index] = call
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Function.Name != "" {
				call.Function.Name = tc.Function.Name
			}
			if len(tc.Extra) > 0 {
				call.Extra = tc.Extra
			}
			call.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stream broke: %w", err)
	}

	msg := wireMessage{Role: "assistant", Content: text.String()}
	for _, i := range order {
		msg.ToolCalls = append(msg.ToolCalls, *calls[i])
	}
	blocks, stop := toBlocks(msg)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("the model returned an empty response")
	}
	return &provider.Response{Content: blocks, StopReason: stop, Usage: usage, Model: model}, nil
}

// parse is split from Send so it can be tested without the network.
func parse(raw []byte, status int) (*provider.Response, error) {
	var wire wireResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("response is not JSON (HTTP %d): %s", status, snippet(raw))
	}
	if wire.Error != nil {
		return nil, fmt.Errorf("provider error (HTTP %d): %s", status, wire.Error.Message)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("HTTP %d: %s", status, snippet(raw))
	}
	if len(wire.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices: %s", snippet(raw))
	}

	blocks, stop := toBlocks(wire.Choices[0].Message)
	return &provider.Response{Content: blocks, StopReason: stop, Model: wire.Model}, nil
}

// toBlocks turns one assistant message into neutral content blocks.
func toBlocks(msg wireMessage) ([]provider.ContentBlock, provider.StopReason) {
	var blocks []provider.ContentBlock
	stop := provider.StopEndTurn
	if msg.Content != "" {
		blocks = append(blocks, provider.ContentBlock{
			Type: provider.BlockText,
			Text: msg.Content,
		})
	}
	for _, call := range msg.ToolCalls {
		args := call.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}" // some models send "" for tools that take no arguments
		}
		blocks = append(blocks, provider.ContentBlock{
			Type:      provider.BlockToolUse,
			ToolUseID: call.ID,
			ToolName:  call.Function.Name,
			ToolInput: json.RawMessage(args),
			Signature: string(call.Extra),
		})
		stop = provider.StopToolUse
	}
	return blocks, stop
}

func snippet(raw []byte) string {
	const max = 300
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}
