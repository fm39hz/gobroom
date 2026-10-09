package anthropic

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func overlayNativeAnthropicMutations(body map[string]any, request kernel.NormalizedRequest) error {
	mutations := request.Mutations
	if mutations.OperationPayload || mutations.Modalities || mutations.Requirements {
		return fmt.Errorf("Anthropic Messages egress cannot safely project transformed operation payload, modality or requirement facets")
	}
	if mutations.Prompt {
		system, err := overlayAnthropicSystem(body["system"], request.Prompt)
		if err != nil {
			return err
		}
		if system == nil {
			delete(body, "system")
		} else {
			body["system"] = system
		}
	}
	if mutations.Messages {
		messages, err := overlayAnthropicMessages(body["messages"], request.Messages)
		if err != nil {
			return err
		}
		body["messages"] = messages
	}
	if mutations.Tools {
		if len(request.Tools) == 0 {
			delete(body, "tools")
		} else {
			tools := make([]map[string]any, 0, len(request.Tools))
			for _, tool := range request.Tools {
				item := copyAnthropicObject(tool.Metadata)
				for _, key := range []string{"name", "description", "input_schema"} {
					delete(item, key)
				}
				name := tool.Name
				if candidate, ok := tool.Function["name"].(string); ok && candidate != "" {
					name = candidate
				}
				if name == "" {
					return fmt.Errorf("Anthropic tool overlay requires a name")
				}
				item["name"] = name
				if description, ok := tool.Function["description"].(string); ok {
					item["description"] = description
				}
				if schema, ok := tool.Function["parameters"]; ok {
					item["input_schema"] = schema
				} else {
					item["input_schema"] = map[string]any{"type": "object"}
				}
				tools = append(tools, item)
			}
			body["tools"] = tools
		}
	}
	if mutations.ToolChoice {
		if !anthropicToolChoiceMatchesTools(request) {
			return fmt.Errorf("Anthropic tool choice does not match a transformed tool definition")
		}
		choice, err := overlayAnthropicToolChoice(request.ToolChoice)
		if err != nil {
			return err
		}
		if choice == nil {
			delete(body, "tool_choice")
		} else {
			body["tool_choice"] = choice
		}
	}
	if mutations.GenerationMaxOutput {
		if request.Generation.MaxOutputTokens == nil {
			delete(body, "max_tokens")
		} else {
			body["max_tokens"] = *request.Generation.MaxOutputTokens
		}
	}
	if mutations.GenerationTemperature {
		setAnthropicOptional(body, "temperature", request.Generation.Temperature)
	}
	if mutations.GenerationTopP {
		setAnthropicOptional(body, "top_p", request.Generation.TopP)
	}
	if mutations.GenerationStopSequences {
		if len(request.Generation.StopSequences) == 0 {
			delete(body, "stop_sequences")
		} else {
			body["stop_sequences"] = append([]string(nil), request.Generation.StopSequences...)
		}
	}
	if mutations.Thinking {
		if anthropicHasUnsupportedFacet(request, "reasoning.intent") {
			return fmt.Errorf("Anthropic thinking transform cannot rewrite an unsupported source reasoning envelope")
		}
		delete(body, "thinking")
		delete(body, "output_config")
		for key, value := range normalize.AnthropicReasoning(request.Thinking) {
			body[key] = value
		}
	}
	if mutations.Continuity {
		return fmt.Errorf("Anthropic Messages egress has no declared continuity-field mapping")
	}
	return nil
}

func overlayAnthropicSystem(raw any, prompt normalize.PromptPlan) (any, error) {
	if len(prompt.Layers) != 1 {
		return nil, fmt.Errorf("native Anthropic system overlay requires exactly one prompt layer")
	}
	layer := prompt.Layers[0]
	switch value := raw.(type) {
	case string:
		if len(layer.Parts) > 0 {
			return nil, fmt.Errorf("native Anthropic string system prompt cannot receive structured transformed parts")
		}
		return layer.Text, nil
	case []any:
		parts := make([]any, len(value))
		used := make([]bool, len(layer.Parts))
		for index, rawPart := range value {
			block, ok := rawPart.(map[string]any)
			if !ok {
				parts[index] = rawPart
				continue
			}
			partIndex := findAnthropicPart(layer.Parts, block, used)
			if partIndex < 0 {
				parts[index] = rawPart
				continue
			}
			used[partIndex] = true
			patched, err := patchAnthropicPart(block, layer.Parts[partIndex])
			if err != nil {
				return nil, err
			}
			parts[index] = patched
		}
		for index, usedPart := range used {
			if !usedPart {
				return nil, fmt.Errorf("transformed Anthropic system part %d has no source block to preserve", index)
			}
		}
		if len(layer.Parts) == 0 && layer.Text != "" {
			return nil, fmt.Errorf("native Anthropic structured system prompt has no typed parts for transformed text")
		}
		return parts, nil
	case nil:
		if len(layer.Parts) > 0 {
			return nil, fmt.Errorf("transformed Anthropic system parts have no source envelope")
		}
		return layer.Text, nil
	default:
		return nil, fmt.Errorf("Anthropic system envelope %T cannot be safely overlaid", raw)
	}
}

func overlayAnthropicMessages(raw any, messages []normalize.Message) ([]any, error) {
	rawMessages, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("Anthropic messages envelope %T cannot be safely overlaid", raw)
	}
	if err := validateAnthropicMessageOrder(rawMessages, messages); err != nil {
		return nil, err
	}
	for _, message := range messages {
		if message.Metadata == nil {
			continue
		}
		if parts, ok := message.Content.([]normalize.ContentPart); ok {
			for _, part := range parts {
				if part.Metadata == nil {
					return nil, fmt.Errorf("new Anthropic content parts inside source messages need a typed block insertion policy")
				}
			}
		}
		for _, call := range message.ToolCalls {
			if call.Metadata == nil {
				return nil, fmt.Errorf("new Anthropic tool calls inside source messages need a typed tool-use insertion policy")
			}
		}
	}
	output := make([]any, len(rawMessages))
	for index, rawMessage := range rawMessages {
		source, ok := rawMessage.(map[string]any)
		if !ok {
			output[index] = rawMessage
			continue
		}
		segments := make([]int, 0)
		for messageIndex, message := range messages {
			if reflect.DeepEqual(message.Metadata, source) {
				segments = append(segments, messageIndex)
			}
		}
		if len(segments) == 0 {
			content, _ := source["content"].([]any)
			if !anthropicMessageContainsMappedToolResult(content, messages) {
				if anthropicSourceHasCanonicalBlock(source) {
					return nil, fmt.Errorf("Anthropic source message has canonical blocks missing from transformed IR")
				}
				output[index] = rawMessage
				continue
			}
		}
		patchedMessage := copyAnthropicObject(source)
		if len(segments) > 0 {
			projectedRole := messages[segments[0]].Role
			for _, messageIndex := range segments[1:] {
				if messages[messageIndex].Role != projectedRole {
					return nil, fmt.Errorf("Anthropic source message was split into incompatible transformed roles")
				}
			}
			if projectedRole != "user" && projectedRole != "assistant" {
				return nil, fmt.Errorf("transformed Anthropic source message role %q has no native message mapping", projectedRole)
			}
			if sourceRole, _ := source["role"].(string); projectedRole != sourceRole {
				return nil, fmt.Errorf("Anthropic source message role is immutable")
			}
			if anthropicSourceHasBlockType(source, "tool_use") && projectedRole != "assistant" {
				return nil, fmt.Errorf("Anthropic tool_use history requires the assistant role")
			}
			if anthropicSourceHasBlockType(source, "tool_result") && projectedRole != "user" {
				return nil, fmt.Errorf("Anthropic tool_result history requires the user role")
			}
		}
		content, _ := source["content"].([]any)
		if text, isText := source["content"].(string); isText {
			segment := messages[segments[0]]
			if len(segment.ToolCalls) > 0 {
				return nil, fmt.Errorf("Anthropic scalar message unexpectedly contains tool calls")
			}
			if updatedText, ok := segment.Content.(string); ok {
				patchedMessage["content"] = updatedText
			} else {
				patchedMessage["content"] = text
			}
			output[index] = patchedMessage
			continue
		}
		if source["content"] == nil {
			output[index] = rawMessage
			continue
		}
		if content == nil {
			output[index] = rawMessage
			continue
		}
		updatedBlocks := make([]any, len(content))
		usedScalarSegments := map[int]bool{}
		usedParts := map[anthropicPartIndex]bool{}
		usedCalls := map[anthropicCallIndex]bool{}
		for blockIndex, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				updatedBlocks[blockIndex] = rawBlock
				continue
			}
			typ := anthropicString(block["type"])
			switch typ {
			case "text", "image", "thinking", "redacted_thinking":
				part, partIndex, found := findAnthropicMessagePart(messages, segments, block, usedParts)
				if !found && typ == "text" {
					if text, messageIndex, ok := findAnthropicCollapsedText(messages, segments, usedScalarSegments); ok {
						usedScalarSegments[messageIndex] = true
						patched := copyAnthropicObject(block)
						patched["text"] = text
						updatedBlocks[blockIndex] = patched
						continue
					}
				}
				if !found {
					if typ == "text" || typ == "image" {
						if anthropicBlockHasCanonicalIR(block) {
							return nil, fmt.Errorf("Anthropic content block %d no longer has a canonical IR part", blockIndex)
						}
					}
					updatedBlocks[blockIndex] = rawBlock
					continue
				}
				usedParts[partIndex] = true
				updated, err := patchAnthropicPart(block, part)
				if err != nil {
					return nil, err
				}
				updatedBlocks[blockIndex] = updated
			case "tool_use":
				call, callIndex, found := findAnthropicToolCall(messages, segments, block, usedCalls)
				if !found {
					if anthropicBlockHasCanonicalIR(block) {
						return nil, fmt.Errorf("Anthropic tool_use block %d has no canonical tool-call mapping", blockIndex)
					}
					updatedBlocks[blockIndex] = rawBlock
					continue
				}
				usedCalls[callIndex] = true
				updated := copyAnthropicObject(block)
				updated["input"] = call.Arguments
				updatedBlocks[blockIndex] = updated
			case "tool_result":
				result, found := findAnthropicToolResult(messages, block)
				if !found {
					if anthropicBlockHasCanonicalIR(block) {
						return nil, fmt.Errorf("Anthropic tool_result block %d has no canonical message mapping", blockIndex)
					}
					updatedBlocks[blockIndex] = rawBlock
					continue
				}
				updated := copyAnthropicObject(block)
				if err := patchAnthropicToolResultContent(updated, result.Content); err != nil {
					return nil, err
				}
				updatedBlocks[blockIndex] = updated
			default:
				updatedBlocks[blockIndex] = rawBlock
			}
		}
		for _, messageIndex := range segments {
			message := messages[messageIndex]
			if parts, ok := message.Content.([]normalize.ContentPart); ok {
				for partIndex := range parts {
					if parts[partIndex].Metadata != nil && !usedParts[anthropicPartIndex{message: messageIndex, part: partIndex}] {
						return nil, fmt.Errorf("Anthropic transformed content part has no source block overlay")
					}
				}
			}
			for callIndex, call := range message.ToolCalls {
				if call.Metadata != nil && !usedCalls[anthropicCallIndex{message: messageIndex, call: callIndex}] {
					return nil, fmt.Errorf("Anthropic transformed tool call has no source block overlay")
				}
			}
		}
		patchedMessage["content"] = updatedBlocks
		output[index] = patchedMessage
	}
	for _, message := range messages {
		if message.Metadata != nil {
			continue
		}
		added, err := nativeAnthropicAddedMessage(message)
		if err != nil {
			return nil, err
		}
		output = append(output, added)
	}
	return output, nil
}

func nativeAnthropicAddedMessage(message normalize.Message) (map[string]any, error) {
	if message.Name != "" {
		return nil, fmt.Errorf("new Anthropic messages cannot preserve a separate message name")
	}
	switch message.Role {
	case "user", "assistant":
		if message.Role == "user" && len(message.ToolCalls) > 0 {
			return nil, fmt.Errorf("new Anthropic user messages cannot contain tool calls")
		}
		content, err := nativeAnthropicAddedContent(message.Content)
		if err != nil {
			return nil, err
		}
		if len(message.ToolCalls) == 0 {
			return map[string]any{"role": message.Role, "content": content}, nil
		}
		blocks, ok := content.([]any)
		if !ok {
			blocks = []any{map[string]any{"type": "text", "text": content}}
		}
		for _, call := range message.ToolCalls {
			if call.Metadata != nil {
				return nil, fmt.Errorf("new Anthropic tool calls cannot include opaque provider metadata")
			}
			if call.ID == "" || call.Name == "" {
				return nil, fmt.Errorf("new Anthropic tool calls require stable IDs and names")
			}
			arguments, ok := call.Arguments.(map[string]any)
			if !ok || arguments == nil {
				return nil, fmt.Errorf("new Anthropic tool call %q requires object arguments", call.ID)
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": arguments})
		}
		return map[string]any{"role": message.Role, "content": blocks}, nil
	case "tool":
		if len(message.ToolCalls) != 0 || message.ToolCallID == "" {
			return nil, fmt.Errorf("new Anthropic tool results require a call ID and cannot contain tool calls")
		}
		content, err := nativeAnthropicAddedContent(message.Content)
		if err != nil {
			return nil, err
		}
		return map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": content}}}, nil
	default:
		return nil, fmt.Errorf("new Anthropic message role %q has no native insertion mapping", message.Role)
	}
}

func nativeAnthropicAddedContent(content any) (any, error) {
	switch value := content.(type) {
	case nil:
		return []any{}, nil
	case string:
		return value, nil
	case []normalize.ContentPart:
		blocks := make([]any, 0, len(value))
		for _, part := range value {
			if part.Metadata != nil {
				return nil, fmt.Errorf("new Anthropic content parts cannot include opaque provider metadata")
			}
			switch part.Type {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
			case "image":
				source := map[string]any{}
				if part.URL != "" {
					source["type"], source["url"] = "url", part.URL
				} else if part.Data != "" && part.MediaType != "" {
					source["type"], source["media_type"], source["data"] = "base64", part.MediaType, part.Data
				} else {
					return nil, fmt.Errorf("new Anthropic image part requires a URL or base64 data and media type")
				}
				blocks = append(blocks, map[string]any{"type": "image", "source": source})
			default:
				return nil, fmt.Errorf("new Anthropic content part %q has no native insertion mapping", part.Type)
			}
		}
		return blocks, nil
	default:
		return nil, fmt.Errorf("new Anthropic message content %T has no native insertion mapping", content)
	}
}

// Native overlay patches the original wire items in place to retain provider
// metadata and unsupported blocks. Until it can rebuild that envelope while
// preserving those opaque items, a transform that reorders source messages
// must fail closed instead of silently sending the original order.
func validateAnthropicMessageOrder(rawMessages []any, messages []normalize.Message) error {
	sourceOrder := make(map[string]int, len(rawMessages))
	for index, rawMessage := range rawMessages {
		source, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		encoded, err := json.Marshal(source)
		if err != nil {
			return fmt.Errorf("fingerprint Anthropic source message: %w", err)
		}
		key := string(encoded)
		if _, exists := sourceOrder[key]; exists {
			return fmt.Errorf("duplicate Anthropic source messages have ambiguous canonical provenance")
		}
		sourceOrder[key] = index
	}
	lastSourceIndex := -1
	sawAddition := false
	for _, message := range messages {
		if message.Metadata == nil {
			sawAddition = true
			continue
		}
		if sawAddition {
			return fmt.Errorf("Anthropic message additions must form a suffix so opaque source blocks retain their order")
		}
		encoded, err := json.Marshal(message.Metadata)
		if err != nil {
			return fmt.Errorf("fingerprint transformed Anthropic message: %w", err)
		}
		sourceIndex, found := sourceOrder[string(encoded)]
		if !found {
			continue
		}
		if sourceIndex < lastSourceIndex {
			return fmt.Errorf("Anthropic message reorder cannot be safely overlaid with opaque source blocks")
		}
		lastSourceIndex = sourceIndex
	}
	return nil
}

type anthropicPartIndex struct{ message, part int }
type anthropicCallIndex struct{ message, call int }

func findAnthropicMessagePart(messages []normalize.Message, segments []int, block map[string]any, used map[anthropicPartIndex]bool) (normalize.ContentPart, anthropicPartIndex, bool) {
	for _, messageIndex := range segments {
		message := messages[messageIndex]
		parts, ok := message.Content.([]normalize.ContentPart)
		if !ok {
			continue
		}
		for partIndex, part := range parts {
			ref := anthropicPartIndex{message: messageIndex, part: partIndex}
			if !used[ref] && reflect.DeepEqual(part.Metadata, block) {
				return part, ref, true
			}
		}
	}
	return normalize.ContentPart{}, anthropicPartIndex{}, false
}

func findAnthropicCollapsedText(messages []normalize.Message, segments []int, used map[int]bool) (string, int, bool) {
	for _, messageIndex := range segments {
		if used[messageIndex] {
			continue
		}
		if text, ok := messages[messageIndex].Content.(string); ok {
			return text, messageIndex, true
		}
	}
	return "", 0, false
}

func findAnthropicToolCall(messages []normalize.Message, segments []int, block map[string]any, used map[anthropicCallIndex]bool) (normalize.ToolCall, anthropicCallIndex, bool) {
	for _, messageIndex := range segments {
		for callIndex, call := range messages[messageIndex].ToolCalls {
			ref := anthropicCallIndex{message: messageIndex, call: callIndex}
			if !used[ref] && reflect.DeepEqual(call.Metadata, block) {
				return call, ref, true
			}
		}
	}
	return normalize.ToolCall{}, anthropicCallIndex{}, false
}

func findAnthropicToolResult(messages []normalize.Message, block map[string]any) (normalize.Message, bool) {
	for _, message := range messages {
		if message.Role == "tool" && reflect.DeepEqual(message.Metadata, block) {
			return message, true
		}
	}
	return normalize.Message{}, false
}

func anthropicMessageContainsMappedToolResult(blocks []any, messages []normalize.Message) bool {
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if ok && anthropicString(block["type"]) == "tool_result" {
			if _, found := findAnthropicToolResult(messages, block); found {
				return true
			}
		}
	}
	return false
}

func anthropicSourceHasCanonicalBlock(message map[string]any) bool {
	if _, ok := message["content"].(string); ok {
		return true
	}
	blocks, _ := message["content"].([]any)
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok && anthropicBlockHasCanonicalIR(block) {
			return true
		}
	}
	return false
}

func anthropicSourceHasBlockType(message map[string]any, wanted string) bool {
	blocks, _ := message["content"].([]any)
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok && anthropicString(block["type"]) == wanted {
			return true
		}
	}
	return false
}

func anthropicBlockHasCanonicalIR(block map[string]any) bool {
	switch anthropicString(block["type"]) {
	case "text":
		_, hasText := block["text"].(string)
		return hasText && anthropicOnlyKeysLocal(block, "type", "text", "cache_control")
	case "image":
		if !anthropicOnlyKeysLocal(block, "type", "source", "cache_control") {
			return false
		}
		source, _ := block["source"].(map[string]any)
		switch anthropicString(source["type"]) {
		case "base64":
			_, mediaOK := source["media_type"].(string)
			_, dataOK := source["data"].(string)
			return mediaOK && dataOK && anthropicOnlyKeysLocal(source, "type", "media_type", "data")
		case "url":
			_, urlOK := source["url"].(string)
			return urlOK && anthropicOnlyKeysLocal(source, "type", "url")
		}
	case "tool_use":
		return anthropicOnlyKeysLocal(block, "type", "id", "name", "input")
	case "tool_result":
		return anthropicOnlyKeysLocal(block, "type", "tool_use_id", "content", "is_error")
	case "thinking", "redacted_thinking":
		return true
	}
	return false
}

func anthropicOnlyKeysLocal(value map[string]any, allowed ...string) bool {
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

func patchAnthropicPart(source map[string]any, part normalize.ContentPart) (map[string]any, error) {
	updated := copyAnthropicObject(source)
	switch part.Type {
	case "text":
		updated["text"] = part.Text
	case "image":
		originalSource, _ := source["source"].(map[string]any)
		imageSource := copyAnthropicObject(originalSource)
		if part.URL != "" {
			imageSource["type"], imageSource["url"] = "url", part.URL
			delete(imageSource, "data")
			delete(imageSource, "media_type")
		} else if part.Data != "" && part.MediaType != "" {
			imageSource["type"], imageSource["media_type"], imageSource["data"] = "base64", part.MediaType, part.Data
			delete(imageSource, "url")
		} else {
			return nil, fmt.Errorf("Anthropic image transform has no typed URL or data source")
		}
		updated["source"] = imageSource
	case "thinking", "redacted_thinking":
		// Signed/opaque thinking is immutable; keep its exact provider block.
	default:
		return nil, fmt.Errorf("Anthropic content part %q has no native block overlay", part.Type)
	}
	return updated, nil
}

func patchAnthropicToolResultContent(block map[string]any, content any) error {
	if text, isString := content.(string); isString {
		if rawParts, ok := block["content"].([]any); ok && len(rawParts) == 1 {
			if part, ok := rawParts[0].(map[string]any); ok && anthropicString(part["type"]) == "text" {
				updated := copyAnthropicObject(part)
				updated["text"] = text
				block["content"] = []any{updated}
				return nil
			}
		}
		block["content"] = text
		return nil
	}
	if content == nil {
		delete(block, "content")
		return nil
	}
	parts, ok := content.([]normalize.ContentPart)
	if !ok {
		return fmt.Errorf("Anthropic tool_result content %T has no block overlay", content)
	}
	rawParts, ok := block["content"].([]any)
	if !ok || len(rawParts) != len(parts) {
		return fmt.Errorf("Anthropic tool_result block shape changed and cannot be safely overlaid")
	}
	updatedParts := make([]any, len(rawParts))
	for index, raw := range rawParts {
		original, ok := raw.(map[string]any)
		if !ok || !reflect.DeepEqual(original, parts[index].Metadata) {
			return fmt.Errorf("Anthropic tool_result part %d lost its source metadata", index)
		}
		updated, err := patchAnthropicPart(original, parts[index])
		if err != nil {
			return err
		}
		updatedParts[index] = updated
	}
	block["content"] = updatedParts
	return nil
}

func findAnthropicPart(parts []normalize.ContentPart, block map[string]any, used []bool) int {
	for index, part := range parts {
		if used != nil && used[index] {
			continue
		}
		if reflect.DeepEqual(part.Metadata, block) {
			return index
		}
	}
	return -1
}

func setAnthropicOptional(body map[string]any, key string, value *float64) {
	if value == nil {
		delete(body, key)
		return
	}
	body[key] = *value
}

func overlayAnthropicToolChoice(choice normalize.ToolChoice) (map[string]any, error) {
	if !anthropicToolChoiceRepresentable(choice) {
		return nil, fmt.Errorf("Anthropic tool choice %q has no native mapping", choice.Mode)
	}
	if !choice.Set {
		return nil, nil
	}
	result := copyAnthropicObject(choice.Metadata)
	result["type"] = choice.Mode
	if choice.Mode == "tool" {
		result["name"] = choice.Name
	} else {
		delete(result, "name")
	}
	if choice.DisableParallelTools {
		result["disable_parallel_tool_use"] = true
	} else {
		delete(result, "disable_parallel_tool_use")
	}
	return result, nil
}

func copyAnthropicObject(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func anthropicString(value any) string {
	result, _ := value.(string)
	return result
}

func anthropicHasUnsupportedFacet(request kernel.NormalizedRequest, facet string) bool {
	for _, unsupported := range request.UnsupportedFacets {
		if unsupported == facet {
			return true
		}
	}
	return false
}
