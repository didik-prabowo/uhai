// Package anthropic is the client for Anthropic's Messages API. It is the
// second wire format ouhai speaks, and the one the neutral types in package
// provider were shaped around: content arrives as blocks, a tool call is a
// block like any other, and the result of running it goes back as one too.
package anthropic

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

// apiVersion is the dated contract this client is written against. Anthropic
// keeps old versions working, so pinning it is what stops a future change
// from arriving unannounced.
const apiVersion = "2023-06-01"

// defaultMaxTokens is the ceiling on one answer when the caller names none.
// The Messages API requires the field, unlike the OpenAI-style one, and a
// request without it is refused outright.
const defaultMaxTokens = 4096

// Options builds a client. BaseURL includes the version, e.g.
// "https://api.anthropic.com/v1".
type Options struct {
	Label   string // shown to the user, e.g. "anthropic"
	BaseURL string
	APIKey  string
	Model   string

	// MaxTokens is the longest answer to ask for. Zero takes the default,
	// which is small enough to be safe on any model.
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

// wireBlock is one piece of content on the wire. The fields are shared by the
// three block types, and which ones are set depends on Type.
type wireBlock struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type wireRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
}

// wireEvent is one server-sent event. The Messages API streams a small state
// machine rather than deltas of a whole object: blocks open, receive pieces,
// and close, and the stop reason arrives near the end.
type wireEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	Message *struct {
		Usage wireUsage `json:"usage"`
	} `json:"message"`

	ContentBlock *wireBlock `json:"content_block"`

	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`

	Usage *wireUsage `json:"usage"`

	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type wireUsage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
}

func (c *Client) Send(ctx context.Context, req provider.Request) (*provider.Response, error) {
	body, err := json.Marshal(wireRequest{
		Model:     c.opts.Model,
		MaxTokens: c.opts.MaxTokens,
		System:    req.System,
		Messages:  toWire(req.Messages),
		Tools:     toWireTools(req.Tools),
		Stream:    true,
	})
	if err != nil {
		return nil, err
	}

	resp, err := c.post(ctx, "/messages", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Errors do not come back as a stream: read the whole body and let parse
	// produce the message.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, parseError(raw, resp.StatusCode)
	}
	return parseStream(resp.Body, req.Stream)
}

// toWire translates neutral messages into the Messages shape. It is close to a
// straight copy: the neutral types were modelled on this API, and it is the
// OpenAI-style one that has to flatten things.
func toWire(msgs []provider.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		var blocks []wireBlock
		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockText:
				if b.Text == "" {
					continue // an empty block is refused, and says nothing anyway
				}
				blocks = append(blocks, wireBlock{Type: "text", Text: b.Text})
			case provider.BlockToolUse:
				input := b.ToolInput
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, wireBlock{
					Type:  "tool_use",
					ID:    b.ToolUseID,
					Name:  b.ToolName,
					Input: input,
				})
			case provider.BlockToolResult:
				// Content must survive being empty: omitempty would drop it,
				// and a tool result without content is refused. It is also
				// better said than left out — the model can tell a command
				// that printed nothing from one that failed silently.
				blocks = append(blocks, wireBlock{
					Type:      "tool_result",
					ToolUseID: b.ToolResultForID,
					Content:   orNoOutput(b.ToolResultText),
					IsError:   b.ToolResultError,
				})
			}
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, wireMessage{Role: string(m.Role), Content: blocks})
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
		out = append(out, wireTool{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.JSONSchema,
		})
	}
	return out
}

// parseStream reads the event stream and assembles one response. Text blocks
// are reported as they arrive; a tool call's arguments arrive as fragments of
// JSON, which are only valid once the block closes, so they are collected
// rather than reported.
func parseStream(body io.Reader, onDelta func(string)) (*provider.Response, error) {
	var (
		blocks []provider.ContentBlock
		args   = map[int]*strings.Builder{}
		usage  provider.Usage
		stop   = provider.StopOther
	)

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // one event can be long
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue // the "event:" lines and the blank ones between events
		}

		var e wireEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &e); err != nil {
			continue // an event we cannot read is not worth killing the answer for
		}
		if e.Error != nil {
			return nil, fmt.Errorf("provider error: %s", e.Error.Message)
		}

		switch e.Type {
		case "message_start":
			if e.Message != nil {
				usage.Input = e.Message.Usage.Input
			}
		case "content_block_start":
			if e.ContentBlock == nil {
				continue
			}
			switch e.ContentBlock.Type {
			case "text":
				blocks = append(blocks, provider.ContentBlock{Type: provider.BlockText})
			case "tool_use":
				blocks = append(blocks, provider.ContentBlock{
					Type:      provider.BlockToolUse,
					ToolUseID: e.ContentBlock.ID,
					ToolName:  e.ContentBlock.Name,
				})
				args[e.Index] = &strings.Builder{}
			}
		case "content_block_delta":
			if e.Delta == nil || len(blocks) == 0 {
				continue
			}
			switch e.Delta.Type {
			case "text_delta":
				blocks[len(blocks)-1].Text += e.Delta.Text
				if onDelta != nil {
					onDelta(e.Delta.Text)
				}
			case "input_json_delta":
				if b := args[e.Index]; b != nil {
					b.WriteString(e.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if b := args[e.Index]; b != nil && len(blocks) > 0 {
				input := strings.TrimSpace(b.String())
				if input == "" {
					input = "{}" // a tool with no arguments sends nothing at all
				}
				blocks[len(blocks)-1].ToolInput = json.RawMessage(input)
			}
		case "message_delta":
			if e.Delta != nil && e.Delta.StopReason != "" {
				stop = toStopReason(e.Delta.StopReason)
			}
			if e.Usage != nil {
				usage.Output = e.Usage.Output
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stream broke: %w", err)
	}

	blocks = dropEmpty(blocks)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("the model returned an empty response")
	}
	return &provider.Response{Content: blocks, StopReason: stop, Usage: usage}, nil
}

// dropEmpty removes text blocks that never received a delta, which is what a
// turn that only calls a tool tends to open with.
func dropEmpty(blocks []provider.ContentBlock) []provider.ContentBlock {
	out := blocks[:0]
	for _, b := range blocks {
		if b.Type == provider.BlockText && b.Text == "" {
			continue
		}
		out = append(out, b)
	}
	return out
}

func toStopReason(s string) provider.StopReason {
	switch s {
	case "tool_use":
		return provider.StopToolUse
	case "end_turn", "stop_sequence":
		return provider.StopEndTurn
	}
	return provider.StopOther // max_tokens, and whatever is added later
}

// parseError turns a failed request into the message the user sees. The body
// carries the reason; the status alone rarely says enough.
func parseError(raw []byte, status int) error {
	var wire struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.BaseURL+"/models?limit=100", nil)
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
		Data []struct {
			ID      string `json:"id"`
			Created string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}

	sort.Slice(wire.Data, func(i, j int) bool { return wire.Data[i].Created > wire.Data[j].Created })
	ids := make([]string, 0, len(wire.Data))
	for _, m := range wire.Data {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("the endpoint returned no models")
	}
	return ids, nil
}

// post sends the request. The body is kept as bytes so every attempt can send
// it again; the retrying itself is shared with the other clients.
func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	return provider.Post(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.BaseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", apiVersion)
	if c.opts.APIKey != "" {
		req.Header.Set("X-Api-Key", c.opts.APIKey)
	}
}
