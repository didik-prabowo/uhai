// Package gemini is the client for Google's generateContent API — the third
// wire format ouhai speaks, and the one that bends the neutral types furthest.
//
// Three differences worth knowing before reading this. The assistant is called
// "model" here. A tool call and its result are parts of a message rather than
// messages or blocks of their own. And a result is matched to its call by the
// tool's *name*, not by an id, so the id every other API hands out has to be
// looked back up while translating.
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

// headerTimeout bounds the wait for the first byte only. A whole-request
// timeout would cut off long answers, which streaming makes normal — the user
// interrupts instead.
const headerTimeout = 60 * time.Second

// listTimeout bounds listing models: that request is not a stream, and nothing
// watches the keyboard while it runs.
const listTimeout = 15 * time.Second

// defaultMaxTokens is the ceiling on one answer when the caller names none.
const defaultMaxTokens = 4096

// Options builds a client. BaseURL includes the version, e.g.
// "https://generativelanguage.googleapis.com/v1beta".
type Options struct {
	Label   string // shown to the user, e.g. "gemini"
	BaseURL string
	APIKey  string
	Model   string

	// MaxTokens is the longest answer to ask for. Zero takes the default.
	MaxTokens int
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
	if o.MaxTokens <= 0 {
		o.MaxTokens = defaultMaxTokens
	}
	return &Client{
		opts: o,
		http: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: headerTimeout}},
	}, nil
}

func (c *Client) Name() string { return c.opts.Label + "/" + c.opts.Model }

// wirePart is one piece of a message: text, a call to a tool, or the result of
// running one. Exactly one field is set.
type wirePart struct {
	Text         string        `json:"text,omitempty"`
	FunctionCall *wireCall     `json:"functionCall,omitempty"`
	FunctionResp *wireResponse `json:"functionResponse,omitempty"`
}

type wireCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type wireResponse struct {
	Name string `json:"name"`
	// Response is an object rather than a string: the API insists, whatever
	// the tool actually returned.
	Response map[string]string `json:"response"`
}

type wireContent struct {
	Role  string     `json:"role,omitempty"` // "user" or "model"
	Parts []wirePart `json:"parts"`
}

type wireTool struct {
	FunctionDeclarations []wireFunction `json:"functionDeclarations"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireRequest struct {
	SystemInstruction *wireContent  `json:"system_instruction,omitempty"`
	Contents          []wireContent `json:"contents"`
	Tools             []wireTool    `json:"tools,omitempty"`
	GenerationConfig  struct {
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	} `json:"generationConfig"`
}

// wireChunk is one server-sent event: a whole candidate answer so far, rather
// than a delta of one.
type wireChunk struct {
	Candidates []struct {
		Content      wireContent `json:"content"`
		FinishReason string      `json:"finishReason"`
	} `json:"candidates"`

	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`

	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (c *Client) Send(ctx context.Context, req provider.Request) (*provider.Response, error) {
	wire := wireRequest{Contents: toWire(req.Messages), Tools: toWireTools(req.Tools)}
	wire.GenerationConfig.MaxOutputTokens = c.opts.MaxTokens
	if req.System != "" {
		wire.SystemInstruction = &wireContent{Parts: []wirePart{{Text: req.System}}}
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}

	// alt=sse asks for one event per chunk; without it the answer arrives as a
	// JSON array that only parses once it is complete.
	resp, err := c.post(ctx, ":streamGenerateContent?alt=sse", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, parseError(raw, resp.StatusCode)
	}
	return parseStream(resp.Body, req.Stream)
}

// toWire translates neutral messages into contents. Two things are done here
// that the other clients do not have to do: the assistant becomes "model", and
// a tool result is matched to its call by name, which means remembering which
// name each call id belonged to as the conversation is walked.
func toWire(msgs []provider.Message) []wireContent {
	names := map[string]string{} // tool call id -> tool name

	out := make([]wireContent, 0, len(msgs))
	for _, m := range msgs {
		content := wireContent{Role: string(m.Role)}
		if m.Role == provider.RoleAssistant {
			content.Role = "model"
		}

		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockText:
				if b.Text == "" {
					continue // an empty part says nothing and is refused
				}
				content.Parts = append(content.Parts, wirePart{Text: b.Text})

			case provider.BlockToolUse:
				names[b.ToolUseID] = b.ToolName
				args := b.ToolInput
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				content.Parts = append(content.Parts, wirePart{
					FunctionCall: &wireCall{Name: b.ToolName, Args: args},
				})

			case provider.BlockToolResult:
				content.Parts = append(content.Parts, wirePart{
					FunctionResp: &wireResponse{
						Name: names[b.ToolResultForID],
						// An object, because the API will not take a string —
						// and never an empty one, for the same reason the
						// other clients say "(no output)".
						Response: map[string]string{"result": orNoOutput(b.ToolResultText)},
					},
				})
			}
		}

		if len(content.Parts) == 0 {
			continue
		}
		out = append(out, content)
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
	var declared []wireFunction
	for _, s := range specs {
		declared = append(declared, wireFunction{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.JSONSchema,
		})
	}
	if len(declared) == 0 {
		return nil
	}
	// One tool object holding every function, which is the shape the API
	// expects — not one object per function.
	return []wireTool{{FunctionDeclarations: declared}}
}

// parseStream reads the event stream and assembles one response. Each chunk
// carries whole parts rather than fragments, so text is appended to the block
// being written and a call arrives complete.
func parseStream(body io.Reader, onDelta func(string)) (*provider.Response, error) {
	var (
		blocks []provider.ContentBlock
		usage  provider.Usage
		stop   = provider.StopEndTurn
		calls  int
	)

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // one chunk can be long
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue // blank lines between events
		}

		var chunk wireChunk
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &chunk); err != nil {
			continue // a chunk we cannot read is not worth killing the answer for
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("provider error: %s", chunk.Error.Message)
		}
		if chunk.UsageMetadata != nil {
			usage = provider.Usage{
				Input:  chunk.UsageMetadata.PromptTokenCount,
				Output: chunk.UsageMetadata.CandidatesTokenCount,
			}
		}
		if len(chunk.Candidates) == 0 {
			continue
		}

		candidate := chunk.Candidates[0]
		for _, part := range candidate.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				calls++
				blocks = append(blocks, provider.ContentBlock{
					Type: provider.BlockToolUse,
					// The API hands out no id, and the agent needs one to
					// match the result to: the name and its turn in the
					// conversation are enough to be unique within it.
					ToolUseID: fmt.Sprintf("%s-%d", part.FunctionCall.Name, calls),
					ToolName:  part.FunctionCall.Name,
					ToolInput: orEmptyObject(part.FunctionCall.Args),
				})

			case part.Text != "":
				if n := len(blocks); n > 0 && blocks[n-1].Type == provider.BlockText {
					blocks[n-1].Text += part.Text
				} else {
					blocks = append(blocks, provider.ContentBlock{Type: provider.BlockText, Text: part.Text})
				}
				if onDelta != nil {
					onDelta(part.Text)
				}
			}
		}

		if candidate.FinishReason != "" {
			stop = toStopReason(candidate.FinishReason)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stream broke: %w", err)
	}

	if len(blocks) == 0 {
		return nil, fmt.Errorf("the model returned an empty response")
	}
	// A finish reason of STOP alongside a call still means the turn is waiting
	// on that call: the reason describes why generating stopped, not what the
	// answer asks for.
	if calls > 0 {
		stop = provider.StopToolUse
	}
	return &provider.Response{Content: blocks, StopReason: stop, Usage: usage}, nil
}

func orEmptyObject(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage("{}")
	}
	return args
}

func toStopReason(reason string) provider.StopReason {
	if reason == "STOP" {
		return provider.StopEndTurn
	}
	return provider.StopOther // MAX_TOKENS, SAFETY, RECITATION, and whatever follows
}

// parseError turns a failed request into the message the user sees.
func parseError(raw []byte, status int) error {
	var wire struct {
		Error *struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &wire); err == nil && wire.Error != nil {
		return fmt.Errorf("provider error (HTTP %d): %s", status, wire.Error.Message)
	}
	return fmt.Errorf("provider error (HTTP %d): %s", status, strings.TrimSpace(string(raw)))
}

// Models lists what the key can reach, newest first, so /model can offer them.
func (c *Client) Models() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.BaseURL+"/models?pageSize=200", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, parseError(raw, resp.StatusCode)
	}

	var wire struct {
		Models []struct {
			Name       string   `json:"name"` // "models/gemini-2.5-flash"
			Supported  []string `json:"supportedGenerationMethods"`
			Deprecated bool     `json:"deprecated"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}

	var ids []string
	for _, m := range wire.Models {
		// Embedding and other models cannot hold a conversation; offering them
		// would only produce a puzzling error later.
		if m.Deprecated || !contains(m.Supported, "generateContent") {
			continue
		}
		ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("the endpoint returned no models")
	}
	sort.Strings(ids)
	return ids, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// post sends the request. The body is kept as bytes so every attempt can send
// it again; the retrying itself is shared with the other clients.
func (c *Client) post(ctx context.Context, suffix string, body []byte) (*http.Response, error) {
	url := fmt.Sprintf("%s/models/%s%s", c.opts.BaseURL, c.opts.Model, suffix)
	return provider.Post(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.opts.APIKey != "" {
		// The key travels in a header rather than the query string, so it does
		// not end up in anyone's logs.
		req.Header.Set("X-Goog-Api-Key", c.opts.APIKey)
	}
}
