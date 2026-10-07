package normalize

import (
	"encoding/json"

	"github.com/fm39hz/gobroom/internal/extensions"
)

// CloneRequest returns a branch-local copy before a scoped transform mutates
// normalized input. JSON-shaped values are cloned recursively; immutable
// scalar values and typed semantic contracts are copied by value.
func CloneRequest(request Request) Request {
	clone := request
	clone.OperationPayload = append(json.RawMessage(nil), request.OperationPayload...)
	clone.Artifacts = make([]extensions.ArtifactRef, len(request.Artifacts))
	for i, artifact := range request.Artifacts {
		clone.Artifacts[i] = artifact.Clone()
	}
	clone.Messages = make([]Message, len(request.Messages))
	for i, message := range request.Messages {
		clone.Messages[i] = cloneMessage(message)
	}
	clone.Prompt.Layers = make([]PromptLayer, len(request.Prompt.Layers))
	for i, layer := range request.Prompt.Layers {
		layer.Parts = make([]ContentPart, len(layer.Parts))
		for j, part := range request.Prompt.Layers[i].Parts {
			part.Metadata = cloneMap(part.Metadata)
			layer.Parts[j] = part
		}
		clone.Prompt.Layers[i] = layer
	}
	clone.Tools = make([]Tool, len(request.Tools))
	for i, tool := range request.Tools {
		tool.Function = cloneMap(tool.Function)
		tool.Metadata = cloneMap(tool.Metadata)
		clone.Tools[i] = tool
	}
	clone.Continuity.EncryptedContent = cloneMap(request.Continuity.EncryptedContent)
	clone.Session.ProviderState = append(json.RawMessage(nil), request.Session.ProviderState...)
	clone.Requirements = make([]FeatureRequirement, len(request.Requirements))
	for i, requirement := range request.Requirements {
		requirement.Constraints = append(json.RawMessage(nil), requirement.Constraints...)
		clone.Requirements[i] = requirement
	}
	clone.Extensions = cloneMap(request.Extensions)
	clone.Raw = cloneMap(request.Raw)
	return clone
}

func cloneMessage(message Message) Message {
	message.Content = cloneJSONValue(message.Content)
	message.Metadata = cloneMap(message.Metadata)
	message.ToolCalls = append([]ToolCall(nil), message.ToolCalls...)
	for i := range message.ToolCalls {
		message.ToolCalls[i].Arguments = cloneJSONValue(message.ToolCalls[i].Arguments)
		message.ToolCalls[i].ProviderData = append(json.RawMessage(nil), message.ToolCalls[i].ProviderData...)
		message.ToolCalls[i].Metadata = cloneMap(message.ToolCalls[i].Metadata)
	}
	return message
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = cloneJSONValue(value)
	}
	return clone
}

func cloneJSONValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneMap(item)
	case []any:
		clone := make([]any, len(item))
		for i := range item {
			clone[i] = cloneJSONValue(item[i])
		}
		return clone
	case []map[string]any:
		clone := make([]map[string]any, len(item))
		for i := range item {
			clone[i] = cloneMap(item[i])
		}
		return clone
	case json.RawMessage:
		return append(json.RawMessage(nil), item...)
	case []json.RawMessage:
		clone := make([]json.RawMessage, len(item))
		for i := range item {
			clone[i] = append(json.RawMessage(nil), item[i]...)
		}
		return clone
	case []byte:
		return append([]byte(nil), item...)
	case []string:
		return append([]string(nil), item...)
	case map[string]string:
		clone := make(map[string]string, len(item))
		for key, value := range item {
			clone[key] = value
		}
		return clone
	default:
		return value
	}
}
