package normalize

import "time"

type Format string

const (
	FormatOpenAIChat      Format = "openai"
	FormatOpenAIResponses Format = "openai-responses"
	FormatAnthropic       Format = "anthropic"
	FormatGemini          Format = "gemini"
	FormatGeminiCLI       Format = "gemini-cli"
	FormatAntigravity     Format = "antigravity"
	FormatUnknown         Format = "unknown"
)

type Request struct {
	Model        string          `json:"model"`
	SourceFormat Format          `json:"-"`
	Stream       bool            `json:"stream,omitempty"`
	Messages     []Message       `json:"messages,omitempty"`
	Tools        []Tool          `json:"tools,omitempty"`
	Thinking     ThinkingIntent  `json:"thinking,omitempty"`
	Session      SessionContext  `json:"-"`
	Continuity   ContinuityState `json:"-"`
	Modalities   Modalities      `json:"-"`
	Transport    TransportHints  `json:"-"`
	Extensions   map[string]any  `json:"-"`
	Raw          map[string]any  `json:"-"`
}

type Message struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	Metadata   map[string]any `json:"-"`
}

type ContentPart struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	URL       string         `json:"url,omitempty"`
	MediaType string         `json:"media_type,omitempty"`
	Data      string         `json:"data,omitempty"`
	Metadata  map[string]any `json:"-"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name,omitempty"`
	Arguments any    `json:"arguments,omitempty"`
}

type Tool struct {
	Type     string         `json:"type"`
	Name     string         `json:"name,omitempty"`
	Function map[string]any `json:"function,omitempty"`
	Metadata map[string]any `json:"-"`
}

type ThinkingIntent struct {
	Mode         string
	Effort       string
	BudgetTokens int
	Source       string
}

type SessionContext struct {
	ID           string
	Client       string
	Conversation string
}

type ContinuityState struct {
	ResponseID       string
	PreviousResponse string
	EncryptedContent map[string]any
}

type Modalities struct {
	Vision     bool
	AudioInput bool
	VideoInput bool
	PDF        bool
}

type TransportHints struct {
	AcceptJSON            bool
	AcceptSSE             bool
	ForceStream           bool
	TargetFormat          Format
	PreferredConnectionID string
}

type Result struct {
	Request    Request
	Warnings   []string
	ReceivedAt time.Time
}
