package normalize

import (
	"encoding/json"
	"time"
)

type Format string
type Operation string

const OperationChatGenerate Operation = "chat.generate"

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
	Model                    string               `json:"model"`
	Operation                Operation            `json:"operation"`
	OperationContractVersion uint64               `json:"operationContractVersion"`
	OperationPayload         json.RawMessage      `json:"operationPayload,omitempty"`
	SourceFormat             Format               `json:"-"`
	Stream                   bool                 `json:"stream,omitempty"`
	Messages                 []Message            `json:"messages,omitempty"`
	Prompt                   PromptPlan           `json:"prompt,omitempty"`
	Tools                    []Tool               `json:"tools,omitempty"`
	Thinking                 ThinkingIntent       `json:"thinking,omitempty"`
	Session                  SessionContext       `json:"-"`
	Continuity               ContinuityState      `json:"-"`
	Modalities               Modalities           `json:"-"`
	Requirements             []FeatureRequirement `json:"-"`
	Transport                TransportHints       `json:"-"`
	Extensions               map[string]any       `json:"-"`
	Raw                      map[string]any       `json:"-"`
}

// FeatureRequirement is an extensible hard requirement derived by ingress or
// an operation extension. Unknown fields are never interpreted by the kernel.
type FeatureRequirement struct {
	ID          string          `json:"id"`
	Constraints json.RawMessage `json:"constraints,omitempty"`
}

// PromptPlan keeps instructions distinct from the conversation transcript.
// Layers retain precedence and origin; adapters choose their wire encoding.
type PromptPlan struct {
	Layers []PromptLayer `json:"layers,omitempty"`
}
type PromptOrigin string

const (
	PromptHarness      PromptOrigin = "harness"
	PromptProvider     PromptOrigin = "provider"
	PromptUser         PromptOrigin = "user"
	PromptConversation PromptOrigin = "conversation"
	PromptInline       PromptOrigin = "inline"
)

type PromptLayer struct {
	Origin PromptOrigin  `json:"origin"`
	Role   string        `json:"role,omitempty"`
	Text   string        `json:"text,omitempty"`
	Parts  []ContentPart `json:"parts,omitempty"`
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
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Name         string          `json:"name,omitempty"`
	Arguments    any             `json:"arguments,omitempty"`
	State        ToolCallState   `json:"-"`
	ProviderData json.RawMessage `json:"-"`
	Metadata     map[string]any  `json:"-"`
}

type ToolCallState string

const (
	ToolCallProposed ToolCallState = "proposed"
	ToolCallRunning  ToolCallState = "running"
	ToolCallResult   ToolCallState = "result"
	ToolCallFailed   ToolCallState = "failed"
)

// Response is the semantic result independent of the client's wire protocol.
// Opaque carries provider-only continuation/signature data without interpreting it.
type Response struct {
	ID         string          `json:"id,omitempty"`
	Model      string          `json:"model,omitempty"`
	Content    []ResponsePart  `json:"content,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      Usage           `json:"usage,omitempty"`
	Opaque     json.RawMessage `json:"opaque,omitempty"`
}
type ResponsePart struct {
	Kind     ResponsePartKind `json:"kind"`
	Text     string           `json:"text,omitempty"`
	Thinking string           `json:"thinking,omitempty"`
	ToolCall *ToolCall        `json:"tool_call,omitempty"`
	Opaque   json.RawMessage  `json:"opaque,omitempty"`
}
type ResponsePartKind string

const (
	ResponseText     ResponsePartKind = "text"
	ResponseThinking ResponsePartKind = "thinking"
	ResponseToolCall ResponsePartKind = "tool_call"
	ResponseOpaque   ResponsePartKind = "opaque"
)

type Usage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
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
	ID            string
	Client        string
	Conversation  string
	ProviderState json.RawMessage
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
