// Package openai adalah klien buat semua provider yang bicara format
// Chat Completions ala OpenAI — Groq, Gemini (endpoint compat), Ollama,
// OpenRouter, Z.ai. Yang beda cuma baseURL, key, dan nama model, jadi satu
// implementasi ini cukup buat semuanya.
package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/didik-prabowo/ouhai/internal/provider"
)

const requestTimeout = 120 * time.Second

// Options buat bikin klien. BaseURL sudah termasuk versi, misalnya
// "https://api.groq.com/openai/v1".
type Options struct {
	Label   string // ditampilkan ke user, misal "groq"
	BaseURL string
	APIKey  string // boleh kosong buat server lokal seperti Ollama
	Model   string
}

type Client struct {
	opts Options
	http *http.Client
}

func New(o Options) (*Client, error) {
	if o.BaseURL == "" {
		return nil, fmt.Errorf("baseURL kosong")
	}
	if o.Model == "" {
		return nil, fmt.Errorf("model kosong")
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	return &Client{opts: o, http: &http.Client{Timeout: requestTimeout}}, nil
}

func (c *Client) Name() string { return c.opts.Label + "/" + c.opts.Model }

// --- bentuk wire OpenAI ---

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
		// Arguments dikirim sebagai string JSON, bukan objek — ini memang
		// bentuk OpenAI, bukan salah ketik.
		Arguments string `json:"arguments"`
	} `json:"function"`
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
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// toWire menerjemahkan pesan netral jadi bentuk OpenAI. Satu Message netral
// bisa jadi beberapa pesan wire: hasil tool wajib satu pesan per tool call.
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
				calls = append(calls, call)
			case provider.BlockToolResult:
				// Hasil tool jadi pesan sendiri dengan role "tool".
				out = append(out, wireMessage{
					Role:       "tool",
					ToolCallID: b.ToolResultForID,
					Content:    b.ToolResultText,
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

func (c *Client) Send(req provider.Request) (*provider.Response, error) {
	body, err := json.Marshal(wireRequest{
		Model:    c.opts.Model,
		Messages: toWire(req.System, req.Messages),
		Tools:    toWireTools(req.Tools),
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, c.opts.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.opts.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parse(raw, resp.StatusCode)
}

// parse dipisah dari Send biar bisa dites tanpa jaringan.
func parse(raw []byte, status int) (*provider.Response, error) {
	var wire wireResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("respons bukan JSON (HTTP %d): %s", status, snippet(raw))
	}
	if wire.Error != nil {
		return nil, fmt.Errorf("provider error (HTTP %d): %s", status, wire.Error.Message)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("HTTP %d: %s", status, snippet(raw))
	}
	if len(wire.Choices) == 0 {
		return nil, fmt.Errorf("respons tanpa choices: %s", snippet(raw))
	}

	choice := wire.Choices[0]
	out := &provider.Response{StopReason: provider.StopEndTurn}
	if choice.Message.Content != "" {
		out.Content = append(out.Content, provider.ContentBlock{
			Type: provider.BlockText,
			Text: choice.Message.Content,
		})
	}
	for _, call := range choice.Message.ToolCalls {
		args := call.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}" // sebagian model kirim string kosong buat tool tanpa argumen
		}
		out.Content = append(out.Content, provider.ContentBlock{
			Type:      provider.BlockToolUse,
			ToolUseID: call.ID,
			ToolName:  call.Function.Name,
			ToolInput: json.RawMessage(args),
		})
		out.StopReason = provider.StopToolUse
	}
	return out, nil
}

func snippet(raw []byte) string {
	const max = 300
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}
