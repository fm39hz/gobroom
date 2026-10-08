package openai

import (
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func encodeAnthropicRequestAsOpenAIResponses(request kernel.NormalizedRequest, model string) (map[string]any, error) {
	if len(request.UnsupportedFacets) > 0 || len(request.Generation.Unsupported) > 0 || !openAIAnthropicPromptRepresentable(request) ||
		!openAIAnthropicToolsRepresentable(request) || !openAIAnthropicHistoryRepresentable(request) ||
		!openAIAnthropicToolChoiceRepresentable(request.ToolChoice) || !openAIAnthropicToolChoiceMatchesTools(request) ||
		!openAIAnthropicResponsesGenerationRepresentable(request.Generation) || !openAIResponsesAnthropicReasoningRepresentable(request.Thinking) {
		return nil, fmt.Errorf("Anthropic request contains semantics that OpenAI Responses cannot represent")
	}

	input := make([]map[string]any, 0, len(request.Prompt.Layers)+len(request.Messages))
	for _, layer := range request.Prompt.Layers {
		role := layer.Role
		if role == "" {
			role = "system"
		}
		if role != "system" && role != "developer" {
			return nil, fmt.Errorf("OpenAI Responses cannot preserve prompt layer role %q", role)
		}
		text, err := anthropicPromptText(layer)
		if err != nil {
			return nil, err
		}
		input = append(input, map[string]any{"type": "message", "role": role, "content": []map[string]any{{"type": "input_text", "text": text}}})
	}
	for _, message := range request.Messages {
		if message.Role == "tool" {
			output, err := anthropicToolOutputText(message.Content)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": output})
			continue
		}
		content, hasContent, err := openAIResponsesContent(message.Content, message.Role)
		if err != nil {
			return nil, fmt.Errorf("encode %s content: %w", message.Role, err)
		}
		if hasContent {
			input = append(input, map[string]any{"type": "message", "role": message.Role, "content": content})
		}
		for _, call := range message.ToolCalls {
			arguments, err := openAIArguments(call.Arguments)
			if err != nil {
				return nil, fmt.Errorf("encode tool call %q arguments: %w", call.Name, err)
			}
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": arguments})
		}
		if !hasContent && len(message.ToolCalls) == 0 {
			input = append(input, map[string]any{"type": "message", "role": message.Role, "content": []map[string]any{{"type": "input_text", "text": ""}}})
		}
	}
	body := map[string]any{"model": model, "stream": request.Stream, "input": input}
	if len(request.Tools) > 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			function := make(map[string]any, len(tool.Function)+1)
			for key, value := range tool.Function {
				function[key] = value
			}
			name := tool.Name
			if candidate, ok := function["name"].(string); ok && candidate != "" {
				name = candidate
			}
			function["name"] = name
			function["type"] = "function"
			tools = append(tools, function)
		}
		body["tools"] = tools
	}
	if request.ToolChoice.Set {
		switch request.ToolChoice.Mode {
		case "auto", "none":
			body["tool_choice"] = request.ToolChoice.Mode
		case "any":
			body["tool_choice"] = "required"
		case "tool":
			body["tool_choice"] = map[string]any{"type": "function", "name": request.ToolChoice.Name}
		}
		if request.ToolChoice.DisableParallelTools {
			body["parallel_tool_calls"] = false
		}
	}
	if request.Generation.MaxOutputTokens != nil {
		body["max_output_tokens"] = *request.Generation.MaxOutputTokens
	}
	if request.Generation.Temperature != nil {
		body["temperature"] = *request.Generation.Temperature
	}
	if request.Generation.TopP != nil {
		body["top_p"] = *request.Generation.TopP
	}
	if request.Thinking.Mode != "" && request.Thinking.Mode != "inherit" {
		if request.Thinking.Mode == "disabled" {
			body["reasoning"] = map[string]any{"effort": "none"}
		} else {
			body["reasoning"] = map[string]any{"effort": request.Thinking.Effort}
		}
	}
	return body, nil
}

func openAIAnthropicResponsesGenerationRepresentable(options normalize.GenerationOptions) bool {
	return len(options.Unsupported) == 0 && len(options.StopSequences) == 0 &&
		(options.Temperature == nil || *options.Temperature >= 0 && *options.Temperature <= 2) &&
		(options.TopP == nil || *options.TopP >= 0 && *options.TopP <= 1) &&
		(options.MaxOutputTokens == nil || *options.MaxOutputTokens > 0)
}

func openAIResponsesAnthropicReasoningRepresentable(intent normalize.ThinkingIntent) bool {
	switch intent.Mode {
	case "", "inherit":
		return true
	case "disabled":
		return intent.Effort == "" && intent.BudgetTokens == 0
	case "level":
		switch intent.Effort {
		case "minimal", "low", "medium", "high", "xhigh":
			return intent.BudgetTokens == 0
		default:
			return false
		}
	default:
		// Anthropic thinking token budgets and adaptive effort without an
		// explicit level are not equivalent to Responses reasoning effort.
		return false
	}
}

func anthropicPromptText(layer normalize.PromptLayer) (string, error) {
	if len(layer.Parts) == 0 {
		return layer.Text, nil
	}
	var text strings.Builder
	for _, part := range layer.Parts {
		if part.Type != "text" || part.Metadata["cache_control"] != nil {
			return "", fmt.Errorf("OpenAI Responses cannot encode prompt part %q", part.Type)
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

func openAIResponsesContent(value any, role string) ([]map[string]any, bool, error) {
	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}
	if value == nil {
		return nil, false, nil
	}
	if text, ok := value.(string); ok {
		return []map[string]any{{"type": textType, "text": text}}, true, nil
	}
	parts, ok := value.([]normalize.ContentPart)
	if !ok {
		return nil, false, fmt.Errorf("untyped content %T", value)
	}
	content := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Metadata["cache_control"] != nil {
			return nil, false, fmt.Errorf("content cache policy is not representable")
		}
		switch part.Type {
		case "text":
			content = append(content, map[string]any{"type": textType, "text": part.Text})
		case "image":
			if role == "assistant" {
				return nil, false, fmt.Errorf("assistant image output is not representable as Responses input history")
			}
			imageURL := part.URL
			if imageURL == "" && part.Data != "" && part.MediaType != "" {
				imageURL = "data:" + part.MediaType + ";base64," + part.Data
			}
			if imageURL == "" {
				return nil, false, fmt.Errorf("image has no URL or typed base64 source")
			}
			content = append(content, map[string]any{"type": "input_image", "image_url": imageURL})
		default:
			return nil, false, fmt.Errorf("content part %q is not representable", part.Type)
		}
	}
	return content, len(content) > 0, nil
}

func anthropicToolOutputText(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, ok := value.([]normalize.ContentPart)
	if !ok {
		return "", fmt.Errorf("tool result content %T is not representable as Responses function output", value)
	}
	var output strings.Builder
	for _, part := range parts {
		if part.Type != "text" {
			return "", fmt.Errorf("tool result part %q is not representable as Responses function output", part.Type)
		}
		output.WriteString(part.Text)
	}
	return output.String(), nil
}
