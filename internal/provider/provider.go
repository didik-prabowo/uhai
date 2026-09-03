// Package provider defines the contract every LLM vendor (Groq, OpenAI,
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
type ContentBlock struct {
	Type BlockType

	// Set when Type == BlockText
	Text string

	// Set when Type == BlockToolUse
	ToolUseID string
	ToolName  string
	ToolInput json.RawMessage

	// Set when Type == BlockToolResult
	ToolResultForID string
	ToolResultText  string
	ToolResultError bool
}

// Message is one turn in the conversation.
type Message struct {
	Role    Role
	Content []ContentBlock
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
}

// Usage is what one call cost in tokens. Zero means the provider did not say.
type Usage struct {
	Input  int
	Output int
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
	// Name is shown to the user, e.g. "groq/llama-3.3-70b-versatile".
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
