package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Gemini struct{ Client *http.Client }

type requestCodec struct{ adapter Gemini }

func (requestCodec) ID() string { return "gemini-json" }
func (requestCodec) DescribeCompatibility(input kernel.CompatibilityContext) []kernel.FacetMapping {
	request := input.Request
	nativeIngress := request.SourceFormat == normalize.FormatOpenAIChat
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if kernel.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"invocation"}, Disposition: kernel.FacetUnsupported, Reason: "Gemini request codec has no safe mapping for this facet"}
		if !nativeIngress {
			result = append(result, mapping)
			continue
		}
		switch facet {
		case kernel.FacetWireRequest:
			mapping.Disposition = kernel.FacetTranslated
			mapping.Reason = "OpenAI Chat messages are converted to Gemini contents"
		case kernel.FacetPromptLayers:
			if geminiPromptLayersRepresentable(request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "text prompt layers are mapped to Gemini systemInstruction"
			} else {
				mapping.Reason = "Gemini systemInstruction conversion accepts text-only prompt layers"
			}
		case kernel.FacetToolDefinitions:
			if geminiToolDefinitionsRepresentable(request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "function definitions are mapped to Gemini functionDeclarations"
			} else {
				mapping.Reason = "Gemini cannot guarantee strict function-schema semantics or map a declared tool"
			}
		case kernel.FacetToolHistory:
			if geminiToolHistoryRepresentable(request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "tool calls and results are mapped to Gemini functionCall/functionResponse parts"
			} else {
				mapping.Reason = "tool history lacks a representable role, function name or JSON-object arguments"
			}
		case kernel.FacetToolChoice:
			if geminiToolChoiceRepresentable(request.Raw) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "OpenAI tool choice is mapped to Gemini function-calling configuration"
			} else {
				mapping.Reason = "tool choice has no declared Gemini function-calling mapping"
			}
		case kernel.FacetReasoningIntent:
			if _, err := geminiThinkingConfigFor(request.Thinking); err == nil {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "reasoning intent is mapped to Gemini thinkingConfig"
			} else {
				mapping.Reason = err.Error()
			}
		case kernel.FacetVisionInput:
			if geminiVisionInputRepresentable(request) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "base64 image data URIs are mapped to Gemini inlineData"
			} else {
				mapping.Reason = "Gemini vision input requires base64 image data URIs"
			}
		case kernel.FacetGenerationOptions:
			if geminiGenerationOptionsRepresentable(request.Raw) {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "the requested generation options have explicit Gemini mappings"
			} else {
				mapping.Reason = "one or more generation options are unsupported or have different semantics on Gemini"
			}
		}
		result = append(result, mapping)
	}
	return result
}
func (c requestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

func geminiPromptLayersRepresentable(request kernel.NormalizedRequest) bool {
	for _, layer := range request.Prompt.Layers {
		if len(layer.Parts) > 0 {
			return false
		}
	}
	return true
}

func geminiToolDefinitionsRepresentable(request kernel.NormalizedRequest) bool {
	for _, tool := range request.Tools {
		if tool.Type != "" && tool.Type != "function" {
			return false
		}
		function := tool.Function
		if function == nil {
			return false
		}
		if strict, _ := function["strict"].(bool); strict {
			return false
		}
		name, _ := function["name"].(string)
		if name == "" {
			name = tool.Name
		}
		if name == "" {
			return false
		}
	}
	return true
}

func geminiToolHistoryRepresentable(request kernel.NormalizedRequest) bool {
	callNames := make(map[string]string)
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			if call.Name == "" || !geminiArgumentsObject(call.Arguments) {
				return false
			}
			callNames[call.ID] = call.Name
		}
	}
	for _, message := range request.Messages {
		if message.Role == "system" || message.Role == "developer" {
			continue
		}
		if message.Role == "tool" || message.Role == "function" {
			if message.Name == "" && callNames[message.ToolCallID] == "" {
				return false
			}
			continue
		}
		if _, err := geminiRole(message.Role); err != nil {
			return false
		}
	}
	return true
}

func geminiArgumentsObject(value any) bool {
	if value == nil {
		return true
	}
	switch arguments := value.(type) {
	case string:
		var object map[string]json.RawMessage
		return json.Unmarshal([]byte(arguments), &object) == nil && object != nil
	case json.RawMessage:
		var object map[string]json.RawMessage
		return json.Unmarshal(arguments, &object) == nil && object != nil
	case map[string]any:
		return arguments != nil
	default:
		return false
	}
}

func geminiVisionInputRepresentable(request kernel.NormalizedRequest) bool {
	foundImage := false
	valid := true
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				visit(child)
			}
		case map[string]any:
			typeName, _ := item["type"].(string)
			if typeName == "image_url" || typeName == "input_image" || typeName == "image" {
				foundImage = true
				url := ""
				if nested, ok := item["image_url"].(map[string]any); ok {
					url, _ = nested["url"].(string)
				}
				if url == "" {
					url, _ = item["url"].(string)
				}
				valid = valid && strings.HasPrefix(url, "data:image/") && strings.Contains(url, ";base64,")
			}
			for key, child := range item {
				if key != "image_url" || typeName != "image_url" {
					visit(child)
				}
			}
		}
	}
	for _, message := range request.Messages {
		visit(message.Content)
	}
	return foundImage && valid
}

func geminiGenerationOptionsRepresentable(raw map[string]any) bool {
	supported := map[string]bool{
		"temperature": true, "top_p": true, "top_k": true, "max_tokens": true,
		"max_completion_tokens": true, "presence_penalty": true, "frequency_penalty": true,
		"seed": true, "stop": true, "n": true, "response_format": true, "tool_choice": true,
	}
	for key, value := range raw {
		if key == "model" || key == "stream" || key == "messages" || key == "tools" || key == "reasoning_effort" || key == "thinking" || key == "reasoning" {
			continue
		}
		if !supported[key] {
			return false
		}
		if key == "n" {
			if count, ok := value.(float64); ok && count != 1 {
				return false
			}
		}
		if key == "response_format" {
			if format, ok := value.(map[string]any); ok {
				if schema, ok := format["json_schema"].(map[string]any); ok {
					if strict, _ := schema["strict"].(bool); strict {
						return false
					}
				}
			}
		}
	}
	return true
}

func geminiToolChoiceRepresentable(raw map[string]any) bool {
	value, exists := raw["tool_choice"]
	if !exists || value == nil {
		return true
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	_, err = geminiToolMode(encoded)
	return err == nil
}

type responseDecoder struct{ adapter Gemini }

func (responseDecoder) ID() string { return "gemini-json" }
func (responseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{
		kernel.EventContentBlockEnd, kernel.EventTextDelta, kernel.EventThinkingDelta,
		kernel.EventToolCallDelta, kernel.EventUsage, kernel.EventResponseComplete,
	}
}
func (c responseDecoder) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c responseDecoder) Decode(ctx context.Context, response kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	return c.adapter.DecodeResponse(ctx, response, emit, hooks)
}
func (Gemini) ID() string { return "gemini" }
func NewAdapter() kernel.ProviderAdapter {
	request, response := NewCodecs()
	renderers := make(map[normalize.Format]kernel.ResponseRenderer)
	for _, renderer := range egress.Builtins() {
		renderers[renderer.ID()] = renderer
	}
	return kernel.ComposedAdapter{AdapterID: "gemini", Endpoint: kernel.HTTPJSONEndpoint{}, Request: request, Transport: kernel.HTTPTransport{}, Response: response, Renderers: renderers, ProviderFormat: normalize.FormatGemini}
}
func NewCodecs() (kernel.RequestCodec, kernel.ResponseDecoder) {
	adapter := Gemini{}
	return requestCodec{adapter}, responseDecoder{adapter}
}

func (Gemini) DecodeResponse(ctx context.Context, response kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("Gemini response body is empty")
	}
	var totalUsage kernel.UsageEvent
	if strings.Contains(strings.ToLower(response.Headers.Get("content-type")), "text/event-stream") {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		started, finished := false, false
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			var payload geminiGenerateContentResponse
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return fmt.Errorf("decode Gemini SSE event: %w", err)
			}
			if !started {
				started = true
				if hooks.OnFirstByte != nil {
					hooks.OnFirstByte(time.Now())
				}
			}
			if len(payload.Candidates) == 0 {
				continue
			}
			_, events, usage := openAIResponse(payload)
			totalUsage.InputTokens = usage.InputTokens
			if usage.OutputTokens > 0 {
				totalUsage.OutputTokens = usage.OutputTokens
			}
			for _, event := range events {
				if err := emit(event); err != nil {
					return err
				}
			}
			candidate := payload.Candidates[0]
			if candidate.FinishReason != "" {
				finished = true
				finish := openAIFinishReason(candidate.FinishReason)
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventContentBlockEnd, StopReason: finish}); err != nil {
					return err
				}
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseComplete}); err != nil {
					return err
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if !finished {
			return reportError(hooks, io.ErrUnexpectedEOF)
		}
	} else {
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		var payload geminiGenerateContentResponse
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("decode Gemini response: %w", err)
		}
		if len(payload.Candidates) == 0 {
			return fmt.Errorf("Gemini response contains no candidates")
		}
		if hooks.OnFirstByte != nil {
			hooks.OnFirstByte(time.Now())
		}
		_, events, usage := openAIResponse(payload)
		totalUsage = usage
		for _, event := range events {
			if err := emit(event); err != nil {
				return err
			}
		}
		finish := openAIFinishReason(payload.Candidates[0].FinishReason)
		if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventContentBlockEnd, StopReason: finish}); err != nil {
			return err
		}
		if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseComplete}); err != nil {
			return err
		}
	}
	totalUsage.Status = "ok"
	if hooks.OnComplete != nil {
		hooks.OnComplete(totalUsage)
	}
	return nil
}

// Prepare translates the normalized OpenAI Chat contract into Gemini's
// generateContent shape. Only understood OpenAI fields are forwarded: passing
// the raw Chat body through would send `messages` to Gemini and appear to work
// for neither tools nor streaming.
func (Gemini) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	operation := "generateContent"
	if request.Stream {
		operation = "streamGenerateContent?alt=sse"
	}
	endpoint := "/models/" + url.PathEscape(route.ExternalModel) + ":" + operation
	body, err := geminiRequest(request)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if credential.Secret != "" {
		headers.Set("x-goog-api-key", credential.Secret)
	}
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: endpoint, Headers: headers, Body: bytes.NewReader(data)}, nil
}

func geminiRequest(request kernel.NormalizedRequest) (generateContentRequest, error) {
	body := generateContentRequest{Contents: make([]geminiContent, 0, len(request.Messages))}
	systemParts := make([]geminiPart, 0)
	for _, layer := range request.Prompt.Layers {
		if layer.Text != "" {
			systemParts = append(systemParts, geminiPart{Text: layer.Text})
		}
	}
	var options openAIChatRequestOptions
	if len(request.Raw) > 0 {
		raw, err := json.Marshal(request.Raw)
		if err != nil {
			return generateContentRequest{}, fmt.Errorf("encode OpenAI request options: %w", err)
		}
		if err := decodeStrictJSON(raw, &options); err != nil {
			return generateContentRequest{}, fmt.Errorf("decode OpenAI request options: %w", err)
		}
	}
	if options.Logprobs != nil || options.TopLogprobs != nil || len(options.LogitBias) > 0 || len(options.Functions) > 0 || len(options.FunctionCall) > 0 || options.ParallelToolCalls != nil {
		return generateContentRequest{}, fmt.Errorf("Gemini adapter cannot preserve one or more requested OpenAI options: logprobs, logit_bias, legacy functions/function_call, parallel_tool_calls")
	}
	if len(options.Reasoning) > 0 {
		var reasoning openAIReasoningConfig
		if err := decodeStrictJSON(options.Reasoning, &reasoning); err != nil {
			return generateContentRequest{}, fmt.Errorf("decode OpenAI reasoning options: %w", err)
		}
	}
	if len(options.Thinking) > 0 {
		var thinking openAIThinkingConfig
		if err := decodeStrictJSON(options.Thinking, &thinking); err != nil {
			return generateContentRequest{}, fmt.Errorf("decode thinking options: %w", err)
		}
	}
	switch {
	case options.User != "", len(options.StreamOptions) > 0, options.ServiceTier != "", options.Metadata != nil, options.PromptCacheKey != "", options.SafetyIdentifier != "":
		return generateContentRequest{}, fmt.Errorf("Gemini adapter does not yet support OpenAI request attribution, stream_options, or service-tier options")
	case options.Store != nil && *options.Store:
		return generateContentRequest{}, fmt.Errorf("Gemini adapter cannot honor OpenAI store=true")
	case options.Verbosity != "", len(options.Modalities) > 0, len(options.Audio) > 0, len(options.Prediction) > 0:
		return generateContentRequest{}, fmt.Errorf("Gemini adapter does not yet support OpenAI verbosity, audio, prediction, or output-modality options")
	}
	toolNamesByCallID := map[string]string{}
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			toolNamesByCallID[call.ID] = call.Name
		}
	}
	for _, message := range request.Messages {
		parts, err := messageParts(message)
		if err != nil {
			return generateContentRequest{}, err
		}
		switch message.Role {
		case "system", "developer":
			systemParts = append(systemParts, parts...)
			continue
		case "tool", "function":
			parts = nil
			name := message.Name
			if name == "" {
				name = toolNamesByCallID[message.ToolCallID]
			}
			if name == "" {
				return generateContentRequest{}, fmt.Errorf("Gemini function response is missing its function name")
			}
			response, err := functionResponseContent(message.Content)
			if err != nil {
				return generateContentRequest{}, err
			}
			parts = append(parts, geminiPart{FunctionResponse: &geminiFunctionResponse{ID: message.ToolCallID, Name: name, Response: response}})
			body.Contents = append(body.Contents, geminiContent{Role: "user", Parts: parts})
			continue
		}
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				args, err := toolArguments(call.Arguments)
				if err != nil {
					return generateContentRequest{}, fmt.Errorf("encode Gemini function call %q: %w", call.Name, err)
				}
				part := geminiPart{FunctionCall: &geminiFunctionCall{ID: call.ID, Name: call.Name, Args: args}}
				if metadata, err := json.Marshal(call.Metadata); err == nil {
					var callMetadata struct {
						ThoughtSignature string `json:"thoughtSignature"`
					}
					if json.Unmarshal(metadata, &callMetadata) == nil {
						part.ThoughtSignature = callMetadata.ThoughtSignature
					}
				}
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			role, err := geminiRole(message.Role)
			if err != nil {
				return generateContentRequest{}, err
			}
			body.Contents = append(body.Contents, geminiContent{Role: role, Parts: parts})
		}
	}
	if len(body.Contents) == 0 {
		return generateContentRequest{}, fmt.Errorf("Gemini request requires at least one non-system message")
	}
	if len(systemParts) > 0 {
		body.SystemInstruction = &geminiContent{Parts: systemParts}
	}
	if len(request.Tools) > 0 {
		declarations := make([]geminiFunctionDeclaration, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Type != "" && tool.Type != "function" {
				return generateContentRequest{}, fmt.Errorf("Gemini adapter does not support OpenAI tool type %q", tool.Type)
			}
			functionData, err := json.Marshal(tool.Function)
			if err != nil {
				return generateContentRequest{}, fmt.Errorf("encode tool definition: %w", err)
			}
			var function openAIFunctionDefinition
			if err := decodeStrictJSON(functionData, &function); err != nil {
				return generateContentRequest{}, fmt.Errorf("decode tool definition: %w", err)
			}
			if function.Strict != nil && *function.Strict {
				return generateContentRequest{}, fmt.Errorf("Gemini adapter cannot guarantee OpenAI strict function schema semantics")
			}
			if function.Name == "" {
				function.Name = tool.Name
			}
			if function.Name == "" {
				return generateContentRequest{}, fmt.Errorf("Gemini function declaration is missing its name")
			}
			declarations = append(declarations, geminiFunctionDeclaration{Name: function.Name, Description: function.Description, Parameters: function.Parameters})
		}
		body.Tools = []geminiTool{{FunctionDeclarations: declarations}}
		if choice, err := geminiToolMode(options.ToolChoice); err != nil {
			return generateContentRequest{}, err
		} else if choice != nil {
			body.ToolConfig = &geminiToolConfig{FunctionCallingConfig: *choice}
		}
	} else if choice, err := geminiToolMode(options.ToolChoice); err != nil {
		return generateContentRequest{}, err
	} else if choice != nil && choice.Mode != "NONE" {
		return generateContentRequest{}, fmt.Errorf("OpenAI tool_choice requests tool use but the request contains no tools")
	}

	generation := &geminiGenerationConfig{
		Temperature: options.Temperature, TopP: options.TopP, TopK: options.TopK,
		PresencePenalty: options.PresencePenalty, FrequencyPenalty: options.FrequencyPenalty,
		Seed: options.Seed,
	}
	if options.MaxCompletionTokens != nil {
		generation.MaxOutputTokens = options.MaxCompletionTokens
	} else {
		generation.MaxOutputTokens = options.MaxTokens
	}
	if options.N != nil {
		if *options.N != 1 {
			return generateContentRequest{}, fmt.Errorf("Gemini OpenAI-compat adapter supports n=1, got n=%d", *options.N)
		}
		generation.CandidateCount = options.N
	}
	if options.Stop != nil {
		var stopString string
		if err := json.Unmarshal(options.Stop, &stopString); err == nil {
			generation.StopSequences = []string{stopString}
		} else if err := json.Unmarshal(options.Stop, &generation.StopSequences); err != nil {
			return generateContentRequest{}, fmt.Errorf("decode OpenAI stop sequences: expected string or string array")
		}
	}
	if options.ResponseFormat != nil {
		switch options.ResponseFormat.Type {
		case "text":
			// Text is Gemini's default response mode.
		case "json_object":
			generation.ResponseMimeType = "application/json"
		case "json_schema":
			if options.ResponseFormat.JSONSchema == nil || len(options.ResponseFormat.JSONSchema.Schema) == 0 {
				return generateContentRequest{}, fmt.Errorf("OpenAI json_schema response_format requires a JSON schema")
			}
			if options.ResponseFormat.JSONSchema.Strict != nil && *options.ResponseFormat.JSONSchema.Strict {
				return generateContentRequest{}, fmt.Errorf("Gemini adapter cannot guarantee OpenAI strict JSON schema semantics")
			}
			generation.ResponseMimeType = "application/json"
			generation.ResponseSchema = options.ResponseFormat.JSONSchema.Schema
		default:
			return generateContentRequest{}, fmt.Errorf("Gemini adapter does not support OpenAI response_format %q", options.ResponseFormat.Type)
		}
	}
	thinking, err := geminiThinkingConfigFor(request.Thinking)
	if err != nil {
		return generateContentRequest{}, err
	}
	generation.ThinkingConfig = thinking
	if generation.hasValues() {
		body.GenerationConfig = generation
	}
	return body, nil
}

func messageParts(message normalize.Message) ([]geminiPart, error) {
	if text, ok := message.Content.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []geminiPart{{Text: text}}, nil
	}
	if message.Content == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(message.Content)
	if err != nil {
		return nil, fmt.Errorf("encode message content: %w", err)
	}
	var content []json.RawMessage
	if err := json.Unmarshal(encoded, &content); err != nil {
		var part incomingChatContentPart
		if err := json.Unmarshal(encoded, &part); err != nil {
			return nil, fmt.Errorf("decode message content as Gemini parts: %w", err)
		}
		content = []json.RawMessage{encoded}
	}
	parts := make([]geminiPart, 0, len(content))
	for _, raw := range content {
		var part incomingChatContentPart
		if err := json.Unmarshal(raw, &part); err != nil {
			return nil, fmt.Errorf("decode message content part: %w", err)
		}
		switch part.Type {
		case "text", "input_text":
			parts = append(parts, geminiPart{Text: part.Text})
		case "image_url", "input_image", "image":
			imageURL := part.URL
			if part.ImageURL.URL != "" {
				imageURL = part.ImageURL.URL
			}
			blob, err := inlineImageFromDataURI(imageURL)
			if err != nil {
				return nil, err
			}
			parts = append(parts, geminiPart{InlineData: blob})
		default:
			return nil, fmt.Errorf("Gemini adapter does not support content block type %q", part.Type)
		}
	}
	return parts, nil
}

func inlineImageFromDataURI(value string) (*geminiBlob, error) {
	if !strings.HasPrefix(value, "data:") {
		return nil, fmt.Errorf("Gemini adapter requires image input as a base64 data URI; remote image URLs are not silently dropped")
	}
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return nil, fmt.Errorf("invalid image data URI")
	}
	parts := strings.Split(value[5:comma], ";")
	mediaType, _, err := mime.ParseMediaType(parts[0])
	if err != nil || !strings.Contains(mediaType, "/") {
		return nil, fmt.Errorf("invalid image media type in data URI")
	}
	base64Encoded := false
	for _, parameter := range parts[1:] {
		base64Encoded = base64Encoded || strings.EqualFold(parameter, "base64")
	}
	if !base64Encoded {
		return nil, fmt.Errorf("Gemini adapter only supports base64 image data URIs")
	}
	data, err := base64.StdEncoding.DecodeString(value[comma+1:])
	if err != nil {
		return nil, fmt.Errorf("decode image data URI: %w", err)
	}
	return &geminiBlob{MIMEType: mediaType, Data: base64.StdEncoding.EncodeToString(data)}, nil
}

func functionResponseContent(content any) (json.RawMessage, error) {
	if text, ok := content.(string); ok {
		var decoded json.RawMessage
		if json.Unmarshal([]byte(text), &decoded) == nil && json.Valid(decoded) {
			return decoded, nil
		}
		return json.Marshal(map[string]string{"content": text})
	}
	encoded, err := json.Marshal(content)
	return json.RawMessage(encoded), err
}

func toolArguments(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	var raw json.RawMessage
	switch args := value.(type) {
	case string:
		raw = json.RawMessage(args)
	case json.RawMessage:
		raw = args
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	return raw, nil
}

func geminiToolMode(raw json.RawMessage) (*geminiFunctionCallingConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		switch value {
		case "none":
			return &geminiFunctionCallingConfig{Mode: "NONE"}, nil
		case "required":
			return &geminiFunctionCallingConfig{Mode: "ANY"}, nil
		case "auto":
			return &geminiFunctionCallingConfig{Mode: "AUTO"}, nil
		default:
			return nil, fmt.Errorf("unsupported OpenAI tool_choice %q", value)
		}
	}
	var choice openAIToolChoice
	if err := decodeStrictJSON(raw, &choice); err != nil {
		return nil, fmt.Errorf("decode OpenAI tool_choice: %w", err)
	}
	if choice.Type == "function" || choice.Function.Name != "" {
		if choice.Function.Name == "" {
			return nil, fmt.Errorf("named OpenAI function tool_choice is missing function.name")
		}
		return &geminiFunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: []string{choice.Function.Name}}, nil
	}
	if choice.Type == "allowed_tools" {
		if len(choice.AllowedTools) == 0 {
			return nil, fmt.Errorf("OpenAI allowed_tools choice must include at least one function")
		}
		allowed := make([]string, 0, len(choice.AllowedTools))
		for _, tool := range choice.AllowedTools {
			if tool.Type != "function" || tool.Function.Name == "" {
				return nil, fmt.Errorf("Gemini adapter only supports named function entries in allowed_tools")
			}
			allowed = append(allowed, tool.Function.Name)
		}
		return &geminiFunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: allowed}, nil
	}
	if choice.Type != "" {
		return nil, fmt.Errorf("unsupported OpenAI tool_choice type %q", choice.Type)
	}
	return &geminiFunctionCallingConfig{Mode: "ANY"}, nil
}

func geminiThinkingConfigFor(intent normalize.ThinkingIntent) (*geminiThinkingConfig, error) {
	config := &geminiThinkingConfig{}
	switch intent.Mode {
	case "", "inherit", "auto":
		return nil, nil
	case "disabled":
		budget := 0
		config.ThinkingBudget = &budget
	case "budget", "enabled":
		if intent.BudgetTokens <= 0 {
			return nil, nil
		}
		config.ThinkingBudget = &intent.BudgetTokens
	case "level":
		if intent.Effort == "none" {
			return nil, fmt.Errorf("Gemini does not provide an exact equivalent for reasoning effort %q", intent.Effort)
		}
		if intent.Effort == "" {
			return nil, nil
		}
		config.ThinkingLevel = intent.Effort
	default:
		return nil, nil
	}
	return config, nil
}

func geminiRole(role string) (string, error) {
	switch role {
	case "assistant":
		return "model", nil
	case "user":
		return "user", nil
	default:
		return "", fmt.Errorf("Gemini adapter does not support chat message role %q", role)
	}
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func (config *geminiGenerationConfig) hasValues() bool {
	return config.Temperature != nil || config.TopP != nil || config.TopK != nil || config.MaxOutputTokens != nil || config.PresencePenalty != nil || config.FrequencyPenalty != nil || config.Seed != nil || len(config.StopSequences) > 0 || config.CandidateCount != nil || config.ResponseMimeType != "" || len(config.ResponseSchema) > 0 || config.ThinkingConfig != nil
}

func (a Gemini) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, request.Body)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	req.Header = request.Headers
	response, err := client.Do(req)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	return kernel.UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}

func (Gemini) ClassifyError(status int, _ []byte) kernel.ErrorClass {
	if status == 401 || status == 403 {
		return kernel.ErrorAuth
	}
	if status == 429 || status >= 500 {
		return kernel.ErrorCooldown
	}
	if status >= 400 {
		return kernel.ErrorTerminal
	}
	return kernel.ErrorRetryable
}

func openAIResponse(payload geminiGenerateContentResponse) (openAIChatResponse, []kernel.ResponseEvent, kernel.UsageEvent) {
	created := time.Now().Unix()
	text := strings.Builder{}
	reasoning := strings.Builder{}
	toolCalls := make([]openAIChatToolCall, 0)
	events := make([]kernel.ResponseEvent, 0)
	finish := "stop"
	if len(payload.Candidates) > 0 {
		candidate := payload.Candidates[0]
		finish = openAIFinishReason(candidate.FinishReason)
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				if part.Thought {
					reasoning.WriteString(part.Text)
					events = append(events, kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventThinkingDelta, Text: part.Text})
				} else {
					text.WriteString(part.Text)
					events = append(events, kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventTextDelta, Text: part.Text})
				}
			}
			if call := part.FunctionCall; call != nil {
				arguments := string(call.Args)
				if arguments == "" || arguments == "null" {
					arguments = "{}"
				}
				toolCalls = append(toolCalls, openAIChatToolCall{ID: call.ID, Type: "function", ThoughtSignature: part.ThoughtSignature, Function: openAIChatToolFunction{Name: call.Name, Arguments: arguments}})
				providerData, _ := json.Marshal(map[string]string{"thoughtSignature": part.ThoughtSignature})
				events = append(events, kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventToolCallDelta, Index: len(toolCalls) - 1, ToolCallID: call.ID, ToolName: call.Name, ToolArguments: arguments, Opaque: providerData})
			}
		}
	}
	message := openAIChatMessage{Role: "assistant", Content: text.String()}
	if reasoning.Len() > 0 {
		message.ReasoningContent = reasoning.String()
	}
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
		finish = "tool_calls"
	}
	usage := kernel.UsageEvent{Status: "ok"}
	var outputUsage *openAIChatUsage
	if payload.UsageMetadata != nil {
		usage = geminiUsage(payload.UsageMetadata)
		outputUsage = &openAIChatUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.InputTokens + usage.OutputTokens}
		events = append(events, kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventUsage, Usage: &usage})
	}
	response := openAIChatResponse{ID: "chatcmpl-gemini", Object: "chat.completion", Created: created, Choices: []openAIChatChoice{{Index: 0, Message: message, FinishReason: finish}}, Usage: outputUsage}
	return response, events, usage
}

func geminiUsage(metadata *geminiUsageMetadata) kernel.UsageEvent {
	if metadata == nil {
		return kernel.UsageEvent{Status: "ok"}
	}
	return kernel.UsageEvent{InputTokens: metadata.PromptTokenCount, OutputTokens: metadata.CandidatesTokenCount, Status: "ok"}
}

func openAIFinishReason(reason string) string {
	switch reason {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	case "STOP", "":
		return "stop"
	default:
		return "stop"
	}
}

func reportError(hooks kernel.StreamHooks, err error) error {
	if hooks.OnEvent != nil {
		hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseError, Error: err.Error()})
	}
	if hooks.OnError != nil {
		hooks.OnError(err)
	}
	return err
}
