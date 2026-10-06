package gemini

import "encoding/json"

// Gemini REST wire types keep provider JSON binding out of the kernel and
// make unsupported/missing fields explicit at the adapter boundary.
type generateContentRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiBlob             `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiBlob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig geminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature      *float64              `json:"temperature,omitempty"`
	TopP             *float64              `json:"topP,omitempty"`
	TopK             *int                  `json:"topK,omitempty"`
	MaxOutputTokens  *int                  `json:"maxOutputTokens,omitempty"`
	PresencePenalty  *float64              `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64              `json:"frequencyPenalty,omitempty"`
	Seed             *int64                `json:"seed,omitempty"`
	StopSequences    []string              `json:"stopSequences,omitempty"`
	CandidateCount   *int                  `json:"candidateCount,omitempty"`
	ResponseMimeType string                `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage       `json:"responseSchema,omitempty"`
	ThinkingConfig   *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingBudget *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel  string `json:"thinkingLevel,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates     []geminiCandidate     `json:"candidates"`
	UsageMetadata  *geminiUsageMetadata  `json:"usageMetadata,omitempty"`
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount      int64 `json:"totalTokenCount,omitempty"`
}

type geminiPromptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

type openAIChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Choices []openAIChatChoice `json:"choices"`
	Usage   *openAIChatUsage   `json:"usage,omitempty"`
}

type openAIChatChoice struct {
	Index        int               `json:"index"`
	Message      openAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type openAIChatMessage struct {
	Role             string               `json:"role"`
	Content          string               `json:"content"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIChatToolCall `json:"tool_calls,omitempty"`
}

type openAIChatToolCall struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	ThoughtSignature string                 `json:"thoughtSignature,omitempty"`
	Function         openAIChatToolFunction `json:"function"`
}

type openAIChatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type openAIChatChunk struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Choices []openAIChatDeltaChoice `json:"choices"`
}

type openAIChatDeltaChoice struct {
	Index        int             `json:"index"`
	Delta        openAIChatDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type openAIChatDelta struct {
	Content          *string                   `json:"content,omitempty"`
	ReasoningContent *string                   `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIChatToolCallDelta `json:"tool_calls,omitempty"`
}

type openAIChatToolCallDelta struct {
	Index            int                         `json:"index"`
	ID               string                      `json:"id,omitempty"`
	Type             string                      `json:"type,omitempty"`
	ThoughtSignature string                      `json:"thoughtSignature,omitempty"`
	Function         openAIChatToolFunctionDelta `json:"function"`
}

type openAIChatToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIChatRequestOptions struct {
	Model               string                `json:"model,omitempty"`
	Messages            json.RawMessage       `json:"messages,omitempty"`
	Stream              *bool                 `json:"stream,omitempty"`
	Tools               json.RawMessage       `json:"tools,omitempty"`
	ConversationID      string                `json:"conversation_id,omitempty"`
	ResponseID          string                `json:"response_id,omitempty"`
	PreviousResponseID  string                `json:"previous_response_id,omitempty"`
	Temperature         *float64              `json:"temperature,omitempty"`
	TopP                *float64              `json:"top_p,omitempty"`
	TopK                *int                  `json:"top_k,omitempty"`
	MaxTokens           *int                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                  `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64              `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64              `json:"frequency_penalty,omitempty"`
	Seed                *int64                `json:"seed,omitempty"`
	Stop                json.RawMessage       `json:"stop,omitempty"`
	N                   *int                  `json:"n,omitempty"`
	ResponseFormat      *openAIResponseFormat `json:"response_format,omitempty"`
	ToolChoice          json.RawMessage       `json:"tool_choice,omitempty"`
	Logprobs            *bool                 `json:"logprobs,omitempty"`
	TopLogprobs         *int                  `json:"top_logprobs,omitempty"`
	LogitBias           json.RawMessage       `json:"logit_bias,omitempty"`
	Functions           json.RawMessage       `json:"functions,omitempty"`
	FunctionCall        json.RawMessage       `json:"function_call,omitempty"`
	ParallelToolCalls   *bool                 `json:"parallel_tool_calls,omitempty"`
	User                string                `json:"user,omitempty"`
	StreamOptions       json.RawMessage       `json:"stream_options,omitempty"`
	ServiceTier         string                `json:"service_tier,omitempty"`
	Store               *bool                 `json:"store,omitempty"`
	Metadata            map[string]any        `json:"metadata,omitempty"`
	PromptCacheKey      string                `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier    string                `json:"safety_identifier,omitempty"`
	ReasoningEffort     string                `json:"reasoning_effort,omitempty"`
	Reasoning           json.RawMessage       `json:"reasoning,omitempty"`
	Thinking            json.RawMessage       `json:"thinking,omitempty"`
	Verbosity           string                `json:"verbosity,omitempty"`
	Modalities          json.RawMessage       `json:"modalities,omitempty"`
	Audio               json.RawMessage       `json:"audio,omitempty"`
	Prediction          json.RawMessage       `json:"prediction,omitempty"`
}

type openAIReasoningConfig struct {
	Effort string `json:"effort"`
}

type openAIThinkingConfig struct {
	Type         string `json:"type"`
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type openAIResponseFormat struct {
	Type       string                  `json:"type"`
	JSONSchema *openAIJSONSchemaFormat `json:"json_schema,omitempty"`
}

type openAIJSONSchemaFormat struct {
	Name   string          `json:"name,omitempty"`
	Strict *bool           `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type openAIFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIToolChoice struct {
	Type         string                   `json:"type,omitempty"`
	Function     openAIToolChoiceFunction `json:"function,omitempty"`
	AllowedTools []openAIToolChoiceTool   `json:"allowed_tools,omitempty"`
}

type openAIToolChoiceFunction struct {
	Name string `json:"name"`
}

type openAIToolChoiceTool struct {
	Type     string                   `json:"type"`
	Function openAIToolChoiceFunction `json:"function,omitempty"`
}

type incomingChatContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}
