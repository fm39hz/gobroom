package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func encodeAnthropicRequestAsOpenAIChat(request kernel.NormalizedRequest, model string) (map[string]any, error) {
	if len(request.UnsupportedFacets) > 0 {
		return nil, fmt.Errorf("Anthropic request has unsupported facets: %s", strings.Join(request.UnsupportedFacets, ", "))
	}
	if len(request.Generation.Unsupported) > 0 {
		return nil, fmt.Errorf("OpenAI Chat cannot encode Anthropic options: %s", strings.Join(request.Generation.Unsupported, ", "))
	}
	if !openAIAnthropicPromptRepresentable(request) || !openAIAnthropicToolsRepresentable(request) || !openAIAnthropicHistoryRepresentable(request) || !openAIAnthropicToolChoiceRepresentable(request.ToolChoice) || !openAIAnthropicToolChoiceMatchesTools(request) || !openAIAnthropicGenerationRepresentable(request.Generation) {
		return nil, fmt.Errorf("Anthropic request cannot be represented by the declared OpenAI Chat mapping")
	}
	messages := make([]map[string]any, 0, len(request.Prompt.Layers)+len(request.Messages))
	for _, layer := range request.Prompt.Layers {
		content := layer.Text
		if len(layer.Parts) > 0 {
			var joined strings.Builder
			for _, part := range layer.Parts {
				if part.Type != "text" {
					return nil, fmt.Errorf("OpenAI Chat system prompt cannot encode part %q", part.Type)
				}
				joined.WriteString(part.Text)
			}
			content = joined.String()
		}
		role := layer.Role
		if role == "" {
			role = "system"
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	for _, message := range request.Messages {
		content, err := openAIChatContent(message.Content)
		if err != nil {
			return nil, fmt.Errorf("encode %s message content: %w", message.Role, err)
		}
		encoded := map[string]any{"role": message.Role}
		if content != nil {
			encoded["content"] = content
		}
		if message.Name != "" {
			encoded["name"] = message.Name
		}
		if message.ToolCallID != "" {
			encoded["tool_call_id"] = message.ToolCallID
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				arguments, err := openAIArguments(call.Arguments)
				if err != nil {
					return nil, fmt.Errorf("encode tool call %q arguments: %w", call.Name, err)
				}
				calls = append(calls, map[string]any{
					"id": call.ID, "type": "function",
					"function": map[string]any{"name": call.Name, "arguments": arguments},
				})
			}
			encoded["tool_calls"] = calls
		}
		messages = append(messages, encoded)
	}
	body := map[string]any{"model": model, "stream": request.Stream, "messages": messages}
	if len(request.Tools) > 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			name := tool.Name
			function := map[string]any{}
			for key, value := range tool.Function {
				function[key] = value
			}
			if functionName, ok := function["name"].(string); ok && functionName != "" {
				name = functionName
			}
			if name == "" {
				return nil, fmt.Errorf("Anthropic tool has no name")
			}
			function["name"] = name
			if _, exists := function["parameters"]; !exists {
				return nil, fmt.Errorf("Anthropic tool %q has no input schema", name)
			}
			tools = append(tools, map[string]any{"type": "function", "function": function})
		}
		body["tools"] = tools
	}
	if request.ToolChoice.Set {
		switch request.ToolChoice.Mode {
		case "auto":
			body["tool_choice"] = "auto"
		case "none":
			body["tool_choice"] = "none"
		case "any":
			body["tool_choice"] = "required"
		case "tool":
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": request.ToolChoice.Name}}
		default:
			return nil, fmt.Errorf("unsupported Anthropic tool choice %q", request.ToolChoice.Mode)
		}
		if request.ToolChoice.DisableParallelTools {
			body["parallel_tool_calls"] = false
		}
	}
	if request.Generation.MaxOutputTokens != nil {
		body["max_tokens"] = *request.Generation.MaxOutputTokens
	}
	if request.Generation.Temperature != nil {
		body["temperature"] = *request.Generation.Temperature
	}
	if request.Generation.TopP != nil {
		body["top_p"] = *request.Generation.TopP
	}
	if len(request.Generation.StopSequences) > 0 {
		body["stop"] = append([]string(nil), request.Generation.StopSequences...)
	}
	return body, nil
}

func openAIChatContent(content any) (any, error) {
	if content == nil {
		return nil, nil
	}
	if text, ok := content.(string); ok {
		return text, nil
	}
	parts, ok := content.([]normalize.ContentPart)
	if !ok {
		return nil, fmt.Errorf("untyped content %T is not supported on a translated request", content)
	}
	encoded := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Metadata["cache_control"] != nil {
			return nil, fmt.Errorf("content cache policy is not representable in OpenAI Chat")
		}
		switch part.Type {
		case "text":
			encoded = append(encoded, map[string]any{"type": "text", "text": part.Text})
		case "image":
			imageURL := part.URL
			if imageURL == "" && part.Data != "" && part.MediaType != "" {
				imageURL = "data:" + part.MediaType + ";base64," + part.Data
			}
			if imageURL == "" {
				return nil, fmt.Errorf("image has neither a URL nor a typed base64 source")
			}
			encoded = append(encoded, map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}})
		default:
			return nil, fmt.Errorf("content part %q is not representable in OpenAI Chat", part.Type)
		}
	}
	return encoded, nil
}

func openAIArguments(arguments any) (string, error) {
	if text, ok := arguments.(string); ok {
		if text == "" {
			return "{}", nil
		}
		if !json.Valid([]byte(text)) {
			return "", fmt.Errorf("arguments string is not valid JSON")
		}
		return text, nil
	}
	if arguments == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
