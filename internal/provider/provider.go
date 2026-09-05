// Package provider defines the contract every LLM vendor (OpenAI,
// Anthropic, ...) must satisfy so the agent loop can use it without knowing
// each vendor's request/response format.
package provider

import (
	"context"
	"encoding/json"
)

// Role is who sent a message in the conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType is the kind of content inside a message.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// ContentBlock is a vendor-neutral piece of content: plain text, a request
// to call a tool, or the result of running one.
//
// The json tags spell the names out because these types are written to disk: a
// saved session is a Session whose Messages are these blocks, so without tags
// the file format was whatever the fields happened to be called. Renaming
// ToolUseID — a change that touches no storage code and reads as entirely safe
// — would have brought every saved conversation back with its tool calls
// silently empty. The name on disk is stated now, and a rename leaves it be.
//
// They are capitalised because that is what encoding/json already wrote, and
// the sessions on disk are worth more than tidy-looking tags. Moving to
// snake_case is a migration, not a tidy-up: Go matches keys case-insensitively,
// so "Role" would still find `json:"role"` — but "ToolUseID" would never find
// `json:"tool_use_id"`, and that is exactly the field whose loss says nothing.
type ContentBlock struct {
	Type BlockType `json:"Type"`

	// Set when Type == BlockText
	Text string `json:"Text"`

	// Set when Type == BlockToolUse
	ToolUseID string          `json:"ToolUseID"`
	ToolName  string          `json:"ToolName"`
	ToolInput json.RawMessage `json:"ToolInput"`

	// Set when Type == BlockToolResult
	ToolResultForID string `json:"ToolResultForID"`
	ToolResultText  string `json:"ToolResultText"`
	ToolResultError bool   `json:"ToolResultError"`
}

// Message is one turn in the conversation. Tagged for the same reason as
// ContentBlock: it is the other half of what a saved session is made of.
type Message struct {
	Role    Role           `json:"Role"`
	Content []ContentBlock `json:"Content"`
}

// ToolSpec describes a tool the model may call. JSONSchema uses standard
// JSON Schema, which nearly every vendor understands.
type ToolSpec struct {
	Name        string
	Description string
	JSONSchema  json.RawMessage
}

// StopReason says why the model stopped generating.
type StopReason string

const (
	StopEndTurn StopReason = "end_turn"
	StopToolUse StopReason = "tool_use"
	StopOther   StopReason = "other"
)

// Request is the input of a single provider call.
type Request struct {
	System   string
	Messages []Message
	Tools    []ToolSpec

	// Stream, when set, is called with each piece of assistant text as it
	// arrives. The complete text is still returned in the Response, so a
	// caller that does not want live output simply leaves this nil.
	Stream func(delta string)

	// Reasoning receives a thinking model's working out, which the vendors
	// send apart from the answer and which is not the answer: it is shown
	// differently, it is not kept, and it is never sent back. Without it a
	// model that thinks for twenty seconds looks like a model that has hung.
	Reasoning func(delta string)
}

// Usage is what one call cost in tokens. Zero means the provider did not say.
//
// Cached input is counted apart from Input because it is not billed the same:
// reading a cached prefix is a fraction of the price and writing one is a
// premium on top. Folding them together would make the status row quote a
// figure that is wrong in whichever direction caching happened to work.
type Usage struct {
	Input  int
	Output int

	CacheRead  int // prefix served from cache, billed at a fraction
	CacheWrite int // prefix written to cache, billed at a premium once
}

// Response is the neutral result of a single provider call.
type Response struct {
	Content    []ContentBlock
	StopReason StopReason
	Usage      Usage
}

// Provider is the contract each LLM vendor implements. Adding a vendor means
// adding a type that satisfies this interface — the agent code never changes.
type Provider interface {
	// Name is shown to the user, e.g. "zai/glm-4.7".
	Name() string

	// Send performs one full request and returns a neutral response.
	// Cancelling ctx aborts the in-flight request.
	Send(ctx context.Context, req Request) (*Response, error)
}

// ModelLister is implemented by providers that can list models from their
// endpoint. It is optional so providers without a model-list API still fit
// the core Provider contract.
type ModelLister interface {
	Models() ([]string, error)
}
