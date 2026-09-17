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
	Model        string
	SourceFormat Format
	Stream       bool
	Messages     []Message
	Tools        []Tool
	Thinking     ThinkingIntent
	Session      SessionContext
	Continuity   ContinuityState
	Modalities   Modalities
	Transport    TransportHints
	Extensions   map[string]any
	Raw          map[string]any
}

type Message struct {
	Role       string
	Content    any
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
	Metadata   map[string]any
}

type ContentPart struct {
	Type      string
	Text      string
	URL       string
	MediaType string
	Data      string
	Metadata  map[string]any
}

type ToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments any
}

type Tool struct {
	Type     string
	Name     string
	Function map[string]any
	Metadata map[string]any
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
	AcceptJSON   bool
	AcceptSSE    bool
	ForceStream  bool
	TargetFormat Format
}

type Result struct {
	Request    Request
	Warnings   []string
	ReceivedAt time.Time
}
