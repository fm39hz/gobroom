package openai

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// overlayNativeResponsesMutations applies only canonical facets explicitly
// changed by request transforms. Untouched raw fields remain byte-semantically
// lossless; unsupported semantic rewrites fail closed rather than being lost.
func overlayNativeResponsesMutations(body map[string]any, request kernel.NormalizedRequest) error {
	mutations := request.Mutations
	if mutations.OperationPayload || mutations.Modalities {
		return fmt.Errorf("OpenAI Responses egress cannot yet safely project transformed operation payload or modality facets")
	}
	if mutations.Messages || mutations.Prompt {
		input, err := overlayResponsesInput(body["input"], request)
		if err != nil {
			return err
		}
		body["input"] = input
	}
	if mutations.Tools {
		if len(request.Tools) == 0 {
			delete(body, "tools")
		} else {
			tools := make([]map[string]any, 0, len(request.Tools))
			for _, tool := range request.Tools {
				item := copyObject(tool.Metadata)
				if tool.Type != "" {
					item["type"] = tool.Type
				}
				if tool.Name != "" {
					item["name"] = tool.Name
				}
				if tool.Function != nil {
					for _, key := range []string{"description", "parameters", "strict"} {
						delete(item, key)
					}
					for key, value := range tool.Function {
						item[key] = value
					}
				}
				tools = append(tools, item)
			}
			body["tools"] = tools
		}
	}
	if mutations.ToolChoice {
		choice, err := nativeResponsesToolChoice(request.ToolChoice)
		if err != nil {
			return err
		}
		if choice == nil {
			delete(body, "tool_choice")
		} else {
			body["tool_choice"] = choice
		}
		if request.ToolChoice.DisableParallelTools {
			body["parallel_tool_calls"] = false
		} else {
			body["parallel_tool_calls"] = true
		}
	}
	if mutations.GenerationMaxOutput {
		delete(body, "max_tokens")
		delete(body, "max_completion_tokens")
		delete(body, "max_output_tokens")
		if request.Generation.MaxOutputTokens != nil {
			body["max_output_tokens"] = *request.Generation.MaxOutputTokens
		}
	}
	if mutations.GenerationTemperature {
		setOrDelete(body, "temperature", request.Generation.Temperature)
	}
	if mutations.GenerationTopP {
		setOrDelete(body, "top_p", request.Generation.TopP)
	}
	if mutations.GenerationStopSequences {
		if len(request.Generation.StopSequences) > 0 {
			return fmt.Errorf("OpenAI Responses egress has no declared stop-sequence mapping")
		}
		delete(body, "stop")
		delete(body, "stop_sequences")
	}
	if mutations.Thinking {
		delete(body, "reasoning_effort")
		delete(body, "reasoning")
		for key, value := range normalize.OpenAIResponsesReasoning(request.Thinking) {
			body[key] = value
		}
	}
	if mutations.Continuity {
		setOrDelete(body, "previous_response_id", nonEmptyString(request.Continuity.PreviousResponse))
	}
	return nil
}

func overlayResponsesInput(rawInput any, request kernel.NormalizedRequest) (any, error) {
	var rawItems []any
	singleInputString := false
	switch input := rawInput.(type) {
	case string:
		singleInputString = true
		rawItems = []any{map[string]any{"type": "message", "role": "user", "content": input}}
	case []any:
		rawItems = input
	case nil:
		rawItems = nil
	default:
		return nil, fmt.Errorf("OpenAI Responses input value %T cannot be overlaid from typed IR", rawInput)
	}
	usedMessages := make([]bool, len(request.Messages))
	inlineLayers := map[string][]int{}
	if request.Mutations.Prompt {
		for index, layer := range request.Prompt.Layers {
			if layer.Origin == normalize.PromptInline {
				inlineLayers[layer.Role] = append(inlineLayers[layer.Role], index)
			}
		}
	}
	inlineNext := map[string]int{}
	promptItems := map[int]any{}
	remaining := make([]any, 0, len(rawItems))
	lastMessageIndex := -1
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			remaining = append(remaining, rawItem)
			continue
		}
		role, _ := item["role"].(string)
		switch item["type"] {
		case "function_call":
			if !request.Mutations.ToolCalls && !request.Mutations.Messages {
				remaining = append(remaining, rawItem)
				continue
			}
			messageIndex := findResponsesSourceMessage(request.Messages, item, usedMessages)
			if messageIndex < 0 {
				continue
			}
			message := request.Messages[messageIndex]
			if message.Role != "assistant" {
				return nil, fmt.Errorf("native Responses function_call items require the assistant role")
			}
			if !request.Mutations.ToolCalls {
				mappedCall := false
				for _, call := range message.ToolCalls {
					if reflect.DeepEqual(call.Metadata, item) {
						mappedCall = true
						break
					}
				}
				if !mappedCall {
					continue
				}
				if messageIndex < lastMessageIndex {
					return nil, fmt.Errorf("Responses message reorder cannot be safely mapped to opaque input items")
				}
				lastMessageIndex = messageIndex
				usedMessages[messageIndex] = true
				remaining = append(remaining, rawItem)
				continue
			}
			if len(message.ToolCalls) != 1 {
				return nil, fmt.Errorf("native Responses function_call item lost its canonical call mapping")
			}
			callItem, err := nativeResponsesFunctionCallItem(message.ToolCalls[0])
			if err != nil {
				return nil, err
			}
			if messageIndex < lastMessageIndex {
				return nil, fmt.Errorf("Responses tool-call reorder cannot be safely mapped to opaque input items")
			}
			lastMessageIndex = messageIndex
			usedMessages[messageIndex] = true
			remaining = append(remaining, callItem)
			continue
		case "function_call_output":
			if !request.Mutations.Messages {
				remaining = append(remaining, rawItem)
				continue
			}
			messageIndex := findResponsesSourceMessage(request.Messages, item, usedMessages)
			if messageIndex < 0 {
				continue
			}
			message := request.Messages[messageIndex]
			if message.Role != "tool" {
				return nil, fmt.Errorf("native Responses function_call_output lost its tool-result mapping")
			}
			output, err := nativeResponsesFunctionCallOutput(message.Content)
			if err != nil {
				return nil, err
			}
			patched := copyObject(item)
			patched["output"] = output
			if messageIndex < lastMessageIndex {
				return nil, fmt.Errorf("Responses tool-result reorder cannot be safely mapped to opaque input items")
			}
			lastMessageIndex = messageIndex
			usedMessages[messageIndex] = true
			remaining = append(remaining, patched)
			continue
		}
		if role == "system" || role == "developer" {
			if !request.Mutations.Prompt {
				remaining = append(remaining, rawItem)
				continue
			}
			queue := inlineLayers[role]
			position := inlineNext[role]
			if position < len(queue) {
				layerIndex := queue[position]
				inlineNext[role] = position + 1
				patched := copyObject(item)
				layer := request.Prompt.Layers[layerIndex]
				if len(layer.Parts) == 0 && item["content"] != nil {
					if _, isText := item["content"].(string); !isText {
						return nil, fmt.Errorf("Responses structured inline prompt cannot be overlaid from a text-only prompt layer")
					}
				}
				content, err := responsesPromptContent(layer)
				if err != nil {
					return nil, err
				}
				patched["content"] = content
				promptItems[layerIndex] = patched
				continue
			}
		}
		if request.Mutations.Messages && (role == "user" || role == "assistant" || role == "tool") {
			messageIndex := findResponsesSourceMessage(request.Messages, item, usedMessages)
			if messageIndex < 0 && singleInputString && role == "user" {
				for index, message := range request.Messages {
					if !usedMessages[index] && message.Metadata == nil && message.Role == "user" {
						messageIndex = index
						break
					}
				}
			}
			if messageIndex < 0 {
				return nil, fmt.Errorf("Responses message removal/reordering cannot be safely mapped to raw input items")
			}
			message := request.Messages[messageIndex]
			if message.Role == "tool" {
				return nil, fmt.Errorf("OpenAI Responses egress cannot encode a chat tool-result role as native input")
			}
			if message.Role != "user" && message.Role != "assistant" {
				return nil, fmt.Errorf("transformed Responses message role %q has no native input mapping", message.Role)
			}
			usedMessages[messageIndex] = true
			patched := copyObject(item)
			patched["role"] = message.Role
			content, err := responsesMessageContent(message.Content, message.Role)
			if err != nil {
				return nil, err
			}
			if content == nil {
				delete(patched, "content")
			} else {
				patched["content"] = content
			}
			if messageIndex < lastMessageIndex {
				return nil, fmt.Errorf("Responses message reorder cannot be safely mapped to opaque input items")
			}
			lastMessageIndex = messageIndex
			remaining = append(remaining, patched)
			continue
		}
		remaining = append(remaining, rawItem)
	}

	for role, indices := range inlineLayers {
		if inlineNext[role] != len(indices) {
			return nil, fmt.Errorf("Responses inline prompt layers do not match source input items")
		}
	}
	prefix := make([]any, 0, len(request.Prompt.Layers))
	newMessages := make([]any, 0)
	if request.Mutations.Prompt {
		for index, layer := range request.Prompt.Layers {
			if layer.Origin == normalize.PromptInline {
				item, ok := promptItems[index]
				if !ok {
					return nil, fmt.Errorf("Responses inline prompt layer %d has no source item", index)
				}
				prefix = append(prefix, item)
				continue
			}
			content, err := responsesPromptContent(layer)
			if err != nil {
				return nil, err
			}
			prefix = append(prefix, map[string]any{"type": "message", "role": layer.Role, "content": content})
		}
	}
	if request.Mutations.Messages {
		newMessageSeen := false
		for index, message := range request.Messages {
			if usedMessages[index] {
				continue
			}
			if message.Metadata != nil {
				if newMessageSeen {
					return nil, fmt.Errorf("Responses transformed messages can only append new items after existing history")
				}
				continue
			}
			newMessageSeen = true
			if message.Role == "tool" {
				output, err := nativeResponsesFunctionCallOutput(message.Content)
				if err != nil || message.ToolCallID == "" {
					return nil, fmt.Errorf("new Responses tool result cannot be serialized safely")
				}
				newMessages = append(newMessages, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": output})
				continue
			}
			if message.Role != "user" && message.Role != "assistant" {
				return nil, fmt.Errorf("new Responses message role %q has no registered overlay", message.Role)
			}
			if message.Content != nil {
				content, err := responsesMessageContent(message.Content, message.Role)
				if err != nil {
					return nil, err
				}
				newMessages = append(newMessages, map[string]any{"type": "message", "role": message.Role, "content": content})
			}
			for _, call := range message.ToolCalls {
				callItem, err := nativeResponsesFunctionCallItem(call)
				if err != nil {
					return nil, err
				}
				newMessages = append(newMessages, callItem)
			}
		}
	}
	result := append(prefix, remaining...)
	return append(result, newMessages...), nil
}

func findResponsesSourceMessage(messages []normalize.Message, item map[string]any, used []bool) int {
	for index, message := range messages {
		if !used[index] && reflect.DeepEqual(message.Metadata, item) {
			return index
		}
	}
	return -1
}

func nativeResponsesFunctionCallItem(call normalize.ToolCall) (map[string]any, error) {
	if call.ID == "" || call.Name == "" {
		return nil, fmt.Errorf("Responses function_call requires stable call ID and function name")
	}
	arguments, err := openAIArguments(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("encode Responses function_call arguments: %w", err)
	}
	if _, nestedFunction := call.Metadata["function"]; nestedFunction {
		if call.Type != "" && call.Type != "function" {
			return nil, fmt.Errorf("Chat-style tool-call type %q cannot be mapped to Responses function_call", call.Type)
		}
		for key := range call.Metadata {
			if key != "id" && key != "type" && key != "function" {
				return nil, fmt.Errorf("opaque Chat-style tool-call field %q has no Responses item mapping", key)
			}
		}
		if kind, _ := call.Metadata["type"].(string); kind != "" && kind != "function" {
			return nil, fmt.Errorf("Chat-style tool-call type %q cannot be mapped to Responses function_call", kind)
		}
		function, ok := call.Metadata["function"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Chat-style tool-call function metadata %T is not an object", call.Metadata["function"])
		}
		for key := range function {
			if key != "name" && key != "arguments" {
				return nil, fmt.Errorf("opaque Chat-style function field %q has no Responses item mapping", key)
			}
		}
		// Chat's id/type/function wrapper contains only canonical call identity
		// and payload. Rebuild it from the typed IR instead of nesting it into a
		// Responses item; source and transformed values therefore stay aligned.
		return map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": arguments}, nil
	}
	item := copyObject(call.Metadata)
	item["type"], item["call_id"], item["name"], item["arguments"] = "function_call", call.ID, call.Name, arguments
	return item, nil
}

func nativeResponsesFunctionCallOutput(content any) (any, error) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	if parts, ok := content.([]normalize.ContentPart); ok {
		return anthropicToolOutputText(parts)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("encode Responses function_call_output: %w", err)
	}
	return string(encoded), nil
}

func responsesPromptContent(layer normalize.PromptLayer) (any, error) {
	if len(layer.Parts) == 0 {
		return layer.Text, nil
	}
	return responsesTypedContent(layer.Parts, layer.Role)
}

func responsesMessageContent(content any, role string) (any, error) {
	if content == nil {
		return nil, nil
	}
	if parts, ok := content.([]normalize.ContentPart); ok {
		return responsesTypedContent(parts, role)
	}
	return content, nil
}

func responsesTypedContent(parts []normalize.ContentPart, role string) ([]map[string]any, error) {
	content := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		item := copyObject(part.Metadata)
		switch part.Type {
		case "text":
			item["type"] = "input_text"
			if role == "assistant" {
				item["type"] = "output_text"
			}
			item["text"] = part.Text
		case "image":
			if role == "assistant" {
				return nil, fmt.Errorf("Responses history cannot encode assistant image content")
			}
			imageURL := part.URL
			if imageURL == "" && part.Data != "" && part.MediaType != "" {
				imageURL = "data:" + part.MediaType + ";base64," + part.Data
			}
			if imageURL == "" {
				return nil, fmt.Errorf("Responses image content has no URL or typed data")
			}
			item["type"], item["image_url"] = "input_image", imageURL
		default:
			return nil, fmt.Errorf("Responses content part %q has no registered egress mapping", part.Type)
		}
		content = append(content, item)
	}
	return content, nil
}

func nativeResponsesToolChoice(choice normalize.ToolChoice) (any, error) {
	metadata := copyObject(choice.Metadata)
	if _, hasNestedFunction := metadata["function"]; hasNestedFunction {
		return nil, fmt.Errorf("OpenAI Responses cannot preserve nested function metadata in transformed tool choice")
	}
	if !choice.Set {
		if len(metadata) > 0 {
			return nil, fmt.Errorf("OpenAI Responses cannot encode opaque metadata on an unset tool choice")
		}
		return nil, nil
	}
	switch choice.Mode {
	case "auto", "none", "required":
		if len(metadata) > 0 {
			return nil, fmt.Errorf("OpenAI Responses cannot preserve opaque metadata for scalar tool choice %q", choice.Mode)
		}
		return choice.Mode, nil
	case "any":
		if len(metadata) > 0 {
			return nil, fmt.Errorf("OpenAI Responses cannot preserve opaque metadata for scalar tool choice %q", choice.Mode)
		}
		return "required", nil
	case "tool":
		if choice.Name == "" {
			return nil, fmt.Errorf("OpenAI Responses tool choice requires a name")
		}
		metadata["type"] = "function"
		metadata["name"] = choice.Name
		return metadata, nil
	default:
		return nil, fmt.Errorf("OpenAI Responses cannot encode tool choice %q", choice.Mode)
	}
}

func setOrDelete(body map[string]any, key string, value any) {
	switch typed := value.(type) {
	case nil:
		delete(body, key)
	case *float64:
		if typed == nil {
			delete(body, key)
		} else {
			body[key] = *typed
		}
	case string:
		if typed == "" {
			delete(body, key)
		} else {
			body[key] = typed
		}
	default:
		body[key] = value
	}
}

func nonEmptyString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
