package normalize

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func JSON(path string, headers http.Header, payload []byte) (Result, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return Result{}, fmt.Errorf("invalid JSON: %w", err)
	}
	return Map(path, headers, body)
}

func Map(path string, headers http.Header, body map[string]any) (Result, error) {
	format := DetectWithHeaders(path, body, headers)
	model, _ := body["model"].(string)
	if strings.TrimSpace(model) == "" {
		return Result{}, fmt.Errorf("model is required")
	}

	r := Request{
		Model: model, Operation: OperationChatGenerate, OperationContractVersion: 1, SourceFormat: format, Stream: boolValue(body["stream"], false),
		Tools: normalizeTools(body["tools"], format), ToolChoice: normalizeToolChoice(body["tool_choice"], format),
		Generation: normalizeGenerationOptions(body, format), Extensions: map[string]any{}, Raw: body,
		Transport: TransportHints{AcceptJSON: strings.Contains(strings.ToLower(headers.Get("accept")), "application/json"), AcceptSSE: strings.Contains(strings.ToLower(headers.Get("accept")), "text/event-stream"), PreferredConnectionID: headers.Get("x-connection-id")},
	}
	if format == FormatAnthropic {
		r.Messages, r.UnsupportedFacets = normalizeAnthropicMessages(body)
		if rawTools, ok := body["tools"].([]any); ok {
			for _, rawTool := range rawTools {
				tool, ok := rawTool.(map[string]any)
				if !ok || !anthropicOnlyKeys(tool, "name", "description", "input_schema") {
					r.UnsupportedFacets = append(r.UnsupportedFacets, "tools.definitions")
				}
			}
		}
		if choice, ok := body["tool_choice"].(map[string]any); ok && !anthropicOnlyKeys(choice, "type", "name", "disable_parallel_tool_use") {
			r.UnsupportedFacets = append(r.UnsupportedFacets, "tools.choice")
		}
		if _, hasToolChoice := body["tool_choice"]; hasToolChoice && !r.ToolChoice.Set {
			r.UnsupportedFacets = append(r.UnsupportedFacets, "tools.choice")
		}
		var promptUnsupported []string
		r.Prompt, promptUnsupported = normalizeAnthropicSystem(body["system"], nil)
		r.UnsupportedFacets = append(r.UnsupportedFacets, promptUnsupported...)
		if metadata := mapValue(body["metadata"]); len(metadata) > 0 {
			r.UnsupportedFacets = append(r.UnsupportedFacets, "client.metadata")
		}
	} else {
		r.Messages = normalizeMessages(body, format)
		r.Prompt = inlinePromptPlan(r.Messages)
	}
	r.Messages = conversationMessages(r.Messages)
	if format == FormatAnthropic {
		var valid bool
		r.Thinking, valid = normalizeAnthropicThinking(body)
		if !valid {
			r.UnsupportedFacets = append(r.UnsupportedFacets, "reasoning.intent")
		}
	} else {
		r.Thinking = normalizeThinking(body)
	}
	r.Session = SessionContext{ID: stringValue(headers.Get("x-session-id")), Client: headers.Get("user-agent"), Conversation: stringValue(body["conversation_id"])}
	r.Continuity = normalizeContinuity(body)
	r.Modalities = detectModalities(body)
	for k, v := range body {
		if !isCoreKey(k) && !(format == FormatAnthropic && isAnthropicCoreKey(k)) {
			r.Extensions[k] = v
		}
	}
	normalizeToolCalls(&r)
	return Result{Request: r, ReceivedAt: time.Now()}, nil
}

func inlinePromptPlan(messages []Message) PromptPlan {
	var plan PromptPlan
	for _, message := range messages {
		if message.Role != "system" && message.Role != "developer" {
			continue
		}
		layer := PromptLayer{Origin: PromptInline, Role: message.Role}
		if text, ok := message.Content.(string); ok {
			layer.Text = text
		}
		plan.Layers = append(plan.Layers, layer)
	}
	return plan
}

func conversationMessages(messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != "system" && message.Role != "developer" {
			result = append(result, message)
		}
	}
	return result
}

func normalizeMessages(body map[string]any, format Format) []Message {
	if raw, ok := body["messages"].([]any); ok {
		return messagesFromArray(raw)
	}
	if input, ok := body["input"].([]any); ok {
		return messagesFromArray(input)
	}
	if text, ok := body["input"].(string); ok {
		return []Message{{Role: "user", Content: text}}
	}
	if contents, ok := body["contents"].([]any); ok {
		result := make([]Message, 0, len(contents))
		for _, value := range contents {
			m, _ := value.(map[string]any)
			role := stringValue(m["role"])
			if role == "model" {
				role = "assistant"
			}
			result = append(result, Message{Role: role, Content: m["parts"], Metadata: m})
		}
		return result
	}
	return nil
}

func normalizeAnthropicSystem(value any, unsupported []string) (PromptPlan, []string) {
	if text, ok := value.(string); ok {
		return PromptPlan{Layers: []PromptLayer{{Origin: PromptInline, Role: "system", Text: text}}}, unsupported
	}
	blocks, ok := value.([]any)
	if !ok {
		if value != nil {
			unsupported = append(unsupported, "prompt.layers")
		}
		return PromptPlan{}, unsupported
	}
	if len(blocks) == 0 {
		return PromptPlan{}, unsupported
	}
	parts, rejected := normalizeAnthropicContent(blocks, &unsupported)
	if rejected {
		unsupported = append(unsupported, "prompt.layers")
	}
	return PromptPlan{Layers: []PromptLayer{{Origin: PromptInline, Role: "system", Parts: parts}}}, unsupported
}

func normalizeAnthropicMessages(body map[string]any) ([]Message, []string) {
	rawMessages, _ := body["messages"].([]any)
	messages := make([]Message, 0, len(rawMessages))
	var unsupported []string
	for _, rawMessage := range rawMessages {
		source, ok := rawMessage.(map[string]any)
		if !ok {
			unsupported = append(unsupported, "content.opaque")
			continue
		}
		role := stringValue(source["role"])
		if role != "assistant" && role != "user" && role != "developer" {
			unsupported = append(unsupported, "content.opaque")
		}
		content, _ := source["content"].([]any)
		if text, ok := source["content"].(string); ok {
			messages = append(messages, Message{Role: role, Content: text, Metadata: source})
			continue
		}
		if source["content"] != nil && content == nil {
			unsupported = append(unsupported, "content.opaque")
		}
		var parts []ContentPart
		var calls []ToolCall
		sawToolCall := false
		flush := func() {
			if len(parts) == 0 && len(calls) == 0 {
				return
			}
			messages = append(messages, Message{Role: role, Content: collapseContentParts(parts), ToolCalls: calls, Metadata: source})
			parts = nil
			calls = nil
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				unsupported = append(unsupported, "content.opaque")
				continue
			}
			switch stringValue(block["type"]) {
			case "text", "image":
				if _, hasCachePolicy := block["cache_control"]; hasCachePolicy {
					unsupported = append(unsupported, "content.opaque")
				}
				if sawToolCall && role == "assistant" {
					unsupported = append(unsupported, "tools.block_order")
				}
				part, rejected := anthropicContentPart(block)
				if rejected {
					unsupported = append(unsupported, "content.opaque")
				} else {
					parts = append(parts, part)
				}
			case "tool_use":
				if !anthropicOnlyKeys(block, "type", "id", "name", "input") {
					unsupported = append(unsupported, "content.opaque")
				}
				if role != "assistant" {
					unsupported = append(unsupported, "tools.history")
					continue
				}
				sawToolCall = true
				if stringValue(block["id"]) == "" || stringValue(block["name"]) == "" || mapValue(block["input"]) == nil {
					unsupported = append(unsupported, "tools.history")
				}
				calls = append(calls, ToolCall{
					ID: stringValue(block["id"]), Type: "function", Name: stringValue(block["name"]),
					Arguments: mapValue(block["input"]), State: ToolCallProposed, Metadata: block,
				})
			case "tool_result":
				if !anthropicOnlyKeys(block, "type", "tool_use_id", "content", "is_error") {
					unsupported = append(unsupported, "content.opaque")
				}
				if role != "user" {
					unsupported = append(unsupported, "tools.history")
					continue
				}
				flush()
				if stringValue(block["tool_use_id"]) == "" {
					unsupported = append(unsupported, "tools.history")
				}
				if boolValue(block["is_error"], false) {
					unsupported = append(unsupported, "tools.result_status")
				}
				resultContent, ok := block["content"].([]any)
				if ok {
					resultParts, rejected := normalizeAnthropicContent(resultContent, &unsupported)
					if rejected {
						unsupported = append(unsupported, "content.opaque")
					}
					messages = append(messages, Message{Role: "tool", ToolCallID: stringValue(block["tool_use_id"]), Content: collapseContentParts(resultParts), Metadata: block})
				} else {
					messages = append(messages, Message{Role: "tool", ToolCallID: stringValue(block["tool_use_id"]), Content: block["content"], Metadata: block})
				}
			case "thinking":
				parts = append(parts, ContentPart{Type: "thinking", Text: stringValue(block["thinking"]), Metadata: block})
				unsupported = append(unsupported, "reasoning.signature")
			case "redacted_thinking":
				parts = append(parts, ContentPart{Type: "redacted_thinking", Metadata: block})
				unsupported = append(unsupported, "reasoning.signature", "content.opaque")
			default:
				unsupported = append(unsupported, "content.opaque")
			}
		}
		flush()
		if len(content) == 0 {
			messages = append(messages, Message{Role: role, Metadata: source})
		}
	}
	return messages, uniqueStrings(unsupported)
}

func normalizeAnthropicContent(blocks []any, unsupported *[]string) ([]ContentPart, bool) {
	parts := make([]ContentPart, 0, len(blocks))
	rejected := false
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			*unsupported = append(*unsupported, "content.opaque")
			rejected = true
			continue
		}
		if _, hasCachePolicy := block["cache_control"]; hasCachePolicy {
			*unsupported = append(*unsupported, "content.opaque")
			rejected = true
		}
		part, partRejected := anthropicContentPart(block)
		if partRejected {
			*unsupported = append(*unsupported, "content.opaque")
			rejected = true
			continue
		}
		parts = append(parts, part)
	}
	return parts, rejected
}

func anthropicContentPart(block map[string]any) (ContentPart, bool) {
	switch stringValue(block["type"]) {
	case "text":
		if !anthropicOnlyKeys(block, "type", "text", "cache_control") {
			return ContentPart{}, true
		}
		return ContentPart{Type: "text", Text: stringValue(block["text"]), Metadata: block}, false
	case "image":
		if !anthropicOnlyKeys(block, "type", "source", "cache_control") {
			return ContentPart{}, true
		}
		source := mapValue(block["source"])
		switch stringValue(source["type"]) {
		case "base64":
			if !anthropicOnlyKeys(source, "type", "media_type", "data") {
				return ContentPart{}, true
			}
			return ContentPart{Type: "image", MediaType: stringValue(source["media_type"]), Data: stringValue(source["data"]), Metadata: block}, stringValue(source["media_type"]) == "" || stringValue(source["data"]) == ""
		case "url":
			if !anthropicOnlyKeys(source, "type", "url") {
				return ContentPart{}, true
			}
			return ContentPart{Type: "image", URL: stringValue(source["url"]), Metadata: block}, stringValue(source["url"]) == ""
		default:
			return ContentPart{}, true
		}
	default:
		return ContentPart{}, true
	}
}

func anthropicOnlyKeys(value map[string]any, allowed ...string) bool {
	for key := range value {
		found := false
		for _, accepted := range allowed {
			if key == accepted {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func collapseContentParts(parts []ContentPart) any {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		return parts[0].Text
	}
	return parts
}

func normalizeAnthropicTools(value any) []Tool {
	raw, _ := value.([]any)
	tools := make([]Tool, 0, len(raw))
	for _, item := range raw {
		definition, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := stringValue(definition["name"])
		function := map[string]any{"name": name}
		if description := stringValue(definition["description"]); description != "" {
			function["description"] = description
		}
		if schema, exists := definition["input_schema"]; exists {
			function["parameters"] = schema
		}
		tools = append(tools, Tool{Type: "function", Name: name, Function: function, Metadata: definition})
	}
	return tools
}

func normalizeToolChoice(value any, format Format) ToolChoice {
	if value == nil {
		return ToolChoice{}
	}
	if format == FormatOpenAIChat || format == FormatOpenAIResponses {
		if mode, ok := value.(string); ok {
			if mode == "required" {
				mode = "any"
			}
			return ToolChoice{Mode: mode, Set: true}
		}
		choice, ok := value.(map[string]any)
		if !ok {
			return ToolChoice{}
		}
		function := mapValue(choice["function"])
		mode := stringValue(choice["type"])
		if mode == "function" {
			mode = "tool"
		}
		return ToolChoice{Mode: mode, Name: stringValue(function["name"]), Set: true}
	}
	if format != FormatAnthropic {
		return ToolChoice{}
	}
	choice, ok := value.(map[string]any)
	if !ok {
		return ToolChoice{}
	}
	return ToolChoice{Mode: stringValue(choice["type"]), Name: stringValue(choice["name"]), DisableParallelTools: boolValue(choice["disable_parallel_tool_use"], false), Set: true}
}

func normalizeGenerationOptions(body map[string]any, format Format) GenerationOptions {
	var options GenerationOptions
	if format != FormatAnthropic {
		return options
	}
	if raw, exists := body["max_tokens"]; exists {
		if value, ok := integerPointer(raw); ok && *value > 0 {
			options.MaxOutputTokens = value
		} else {
			options.Unsupported = append(options.Unsupported, "max_tokens")
		}
	}
	if raw, exists := body["temperature"]; exists {
		if value, ok := floatPointer(raw); ok && *value >= 0 && *value <= 1 {
			options.Temperature = value
		} else {
			options.Unsupported = append(options.Unsupported, "temperature")
		}
	}
	if raw, exists := body["top_p"]; exists {
		if value, ok := floatPointer(raw); ok && *value >= 0 && *value <= 1 {
			options.TopP = value
		} else {
			options.Unsupported = append(options.Unsupported, "top_p")
		}
	}
	if raw, exists := body["stop_sequences"]; exists {
		values, ok := raw.([]any)
		if !ok {
			options.Unsupported = append(options.Unsupported, "stop_sequences")
		} else {
			for _, item := range values {
				if value, ok := item.(string); ok {
					options.StopSequences = append(options.StopSequences, value)
				} else {
					options.Unsupported = append(options.Unsupported, "stop_sequences")
				}
			}
		}
	}
	known := map[string]bool{
		"model": true, "messages": true, "system": true, "stream": true, "tools": true,
		"tool_choice": true, "thinking": true, "output_config": true, "metadata": true, "max_tokens": true,
		"temperature": true, "top_p": true, "stop_sequences": true,
	}
	for key := range body {
		if !known[key] {
			options.Unsupported = append(options.Unsupported, key)
		}
	}
	return options
}

func floatPointer(value any) (*float64, bool) {
	number, ok := value.(float64)
	if !ok {
		return nil, false
	}
	return &number, true
}

func integerPointer(value any) (*int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return nil, false
	}
	integer := int(number)
	return &integer, true
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func messagesFromArray(raw []any) []Message {
	result := make([]Message, 0, len(raw))
	for _, value := range raw {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		msg := Message{Role: stringValue(m["role"]), Content: m["content"], Name: stringValue(m["name"]), ToolCallID: stringValue(m["tool_call_id"]), Metadata: m}
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				c, _ := rawCall.(map[string]any)
				fn, _ := c["function"].(map[string]any)
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: stringValue(c["id"]), Type: stringValue(c["type"]), Name: stringValue(fn["name"]), Arguments: fn["arguments"], Metadata: c})
			}
		}
		result = append(result, msg)
	}
	return result
}

func normalizeTools(value any, format Format) []Tool {
	if format == FormatAnthropic {
		return normalizeAnthropicTools(value)
	}
	raw, _ := value.([]any)
	result := make([]Tool, 0, len(raw))
	for _, value := range raw {
		m, ok := value.(map[string]any)
		if ok {
			result = append(result, Tool{Type: stringValue(m["type"]), Name: stringValue(m["name"]), Function: mapValue(m["function"]), Metadata: m})
		}
	}
	return result
}

func normalizeThinking(body map[string]any) ThinkingIntent {
	if effort := stringValue(body["reasoning_effort"]); effort != "" {
		return ThinkingIntent{Mode: "level", Effort: effort, Source: "request_field"}
	}
	if thinking, ok := body["thinking"].(map[string]any); ok {
		return ThinkingIntent{Mode: stringValue(thinking["type"]), Effort: stringValue(thinking["effort"]), BudgetTokens: intValue(thinking["budget_tokens"]), Source: "thinking"}
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		return ThinkingIntent{Mode: "level", Effort: stringValue(reasoning["effort"]), Source: "request_field"}
	}
	return ThinkingIntent{Mode: "inherit", Source: "absent"}
}

func normalizeAnthropicThinking(body map[string]any) (ThinkingIntent, bool) {
	thinking, hasThinking := body["thinking"].(map[string]any)
	outputConfig, hasOutputConfig := body["output_config"].(map[string]any)
	if _, exists := body["thinking"]; exists && !hasThinking {
		return ThinkingIntent{Mode: "inherit", Source: "thinking"}, false
	}
	if _, exists := body["output_config"]; exists && !hasOutputConfig {
		return ThinkingIntent{Mode: "inherit", Source: "output_config"}, false
	}
	if hasOutputConfig && !anthropicOnlyKeys(outputConfig, "effort") {
		return ThinkingIntent{Mode: "inherit", Source: "output_config"}, false
	}
	effort := stringValue(outputConfig["effort"])
	if rawEffort, exists := outputConfig["effort"]; exists && (effort == "" || rawEffort == nil) {
		return ThinkingIntent{Mode: "inherit", Source: "output_config"}, false
	}
	if hasThinking && !anthropicOnlyKeys(thinking, "type", "budget_tokens") {
		return ThinkingIntent{Mode: "inherit", Source: "thinking"}, false
	}
	mode := stringValue(thinking["type"])
	switch mode {
	case "":
		if hasThinking {
			return ThinkingIntent{Mode: "inherit", Source: "thinking"}, false
		}
		if hasOutputConfig {
			return ThinkingIntent{Mode: "inherit", Source: "output_config"}, false
		}
		return ThinkingIntent{Mode: "inherit", Source: "absent"}, true
	case "enabled":
		budget, validBudget := integerPointer(thinking["budget_tokens"])
		if !validBudget || *budget <= 0 || effort != "" {
			if validBudget {
				return ThinkingIntent{Mode: "budget", BudgetTokens: *budget, Source: "thinking"}, false
			}
			return ThinkingIntent{Mode: "budget", Source: "thinking"}, false
		}
		return ThinkingIntent{Mode: "budget", BudgetTokens: *budget, Source: "thinking"}, true
	case "disabled":
		_, hasBudget := thinking["budget_tokens"]
		if hasBudget || effort != "" {
			return ThinkingIntent{Mode: "disabled", Source: "thinking"}, false
		}
		return ThinkingIntent{Mode: "disabled", Source: "thinking"}, true
	case "adaptive":
		_, hasBudget := thinking["budget_tokens"]
		if hasBudget {
			return ThinkingIntent{Mode: "auto", Source: "thinking"}, false
		}
		if effort != "" {
			return ThinkingIntent{Mode: "level", Effort: effort, Source: "output_config"}, true
		}
		return ThinkingIntent{Mode: "auto", Source: "thinking"}, true
	default:
		return ThinkingIntent{Mode: "inherit", Source: "thinking"}, false
	}
}

func normalizeContinuity(body map[string]any) ContinuityState {
	return ContinuityState{ResponseID: stringValue(body["response_id"]), PreviousResponse: stringValue(body["previous_response_id"])}
}

func detectModalities(body map[string]any) Modalities {
	var result Modalities
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			t := strings.ToLower(stringValue(v["type"]))
			result.Vision = result.Vision || t == "image" || t == "image_url" || t == "input_image"
			result.AudioInput = result.AudioInput || t == "audio" || t == "input_audio" || t == "audio_url"
			result.VideoInput = result.VideoInput || t == "video" || t == "input_video" || t == "video_url"
			result.PDF = result.PDF || t == "file" || t == "document" || t == "input_file" || strings.Contains(strings.ToLower(stringValue(v["media_type"])), "pdf")
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		case string:
			lower := strings.ToLower(v)
			result.Vision = result.Vision || strings.Contains(lower, "data:image/")
			result.AudioInput = result.AudioInput || strings.Contains(lower, "data:audio/")
			result.PDF = result.PDF || strings.Contains(lower, "data:application/pdf")
		}
	}
	visit(body)
	return result
}

func normalizeToolCalls(r *Request) {
	sequence := 0
	for i := range r.Messages {
		for j := range r.Messages[i].ToolCalls {
			if r.Messages[i].ToolCalls[j].ID == "" {
				sequence++
				r.Messages[i].ToolCalls[j].ID = fmt.Sprintf("call_gobroom_%d", sequence)
			}
		}
	}
}

func isCoreKey(k string) bool {
	switch k {
	case "model", "messages", "input", "contents", "stream", "tools", "reasoning_effort", "thinking", "reasoning", "conversation_id", "response_id", "previous_response_id", "operationPayload":
		return true
	}
	return false
}

func isAnthropicCoreKey(key string) bool {
	switch key {
	case "system", "tool_choice", "max_tokens", "temperature", "top_p", "stop_sequences", "thinking", "output_config", "metadata":
		return true
	default:
		return false
	}
}
func boolValue(v any, fallback bool) bool {
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}
func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func stringValue(v any) string      { s, _ := v.(string); return s }
func mapValue(v any) map[string]any { m, _ := v.(map[string]any); return m }
