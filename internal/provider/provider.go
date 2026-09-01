// Package provider mendefinisikan kontrak yang harus dipenuhi setiap
// vendor LLM (Groq, OpenAI, Anthropic, dll) supaya bisa dipakai oleh agent
// loop tanpa agent perlu tahu detail format request/response tiap vendor.
package provider

import "encoding/json"

// Role pengirim satu pesan dalam percakapan.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType jenis konten dalam satu pesan.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// ContentBlock adalah representasi netral (gak terikat vendor tertentu)
// untuk satu potong konten: teks biasa, permintaan panggil tool, atau
// hasil eksekusi tool.
type ContentBlock struct {
	Type BlockType

	// Dipakai kalau Type == BlockText
	Text string

	// Dipakai kalau Type == BlockToolUse
	ToolUseID string
	ToolName  string
	ToolInput json.RawMessage

	// Dipakai kalau Type == BlockToolResult
	ToolResultForID string
	ToolResultText  string
	ToolResultError bool
}

// Message adalah satu giliran dalam percakapan.
type Message struct {
	Role    Role
	Content []ContentBlock
}

// ToolSpec mendeskripsikan satu tool yang tersedia untuk dipanggil model.
// JSONSchema pakai format standar JSON Schema, dipahami hampir semua vendor.
type ToolSpec struct {
	Name        string
	Description string
	JSONSchema  json.RawMessage
}

// StopReason kenapa model berhenti generate.
type StopReason string

const (
	StopEndTurn StopReason = "end_turn"
	StopToolUse StopReason = "tool_use"
	StopOther   StopReason = "other"
)

// Request adalah parameter satu kali panggilan ke provider.
type Request struct {
	System   string
	Messages []Message
	Tools    []ToolSpec
}

// Response adalah hasil netral dari satu kali panggilan ke provider.
type Response struct {
	Content    []ContentBlock
	StopReason StopReason
}

// Provider adalah kontrak yang wajib diimplementasikan tiap vendor LLM.
// Nambah vendor baru tinggal bikin struct baru yang implement interface ini —
// tidak perlu ubah kode agent sama sekali.
type Provider interface {
	// Name buat ditampilkan ke user, misal "groq/llama-3.3-70b-versatile".
	Name() string

	// Send mengirim satu request lengkap dan mengembalikan response netral.
	Send(req Request) (*Response, error)
}
