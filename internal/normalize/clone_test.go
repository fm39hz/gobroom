package normalize

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestCloneRequestIsolatesNestedTransformMutation(t *testing.T) {
	request := Request{
		Messages: []Message{{
			Role:      "user",
			Content:   []any{map[string]any{"type": "text", "text": "original"}},
			ToolCalls: []ToolCall{{ID: "call-1", Arguments: map[string]any{"nested": []any{"original"}}, ProviderData: json.RawMessage(`{"sig":"x"}`)}},
		}},
		Prompt: PromptPlan{Layers: []PromptLayer{{Origin: PromptInline, Parts: []ContentPart{{Type: "text", Text: "prompt", Metadata: map[string]any{"tags": []any{"original"}}}}}}},
		Raw:    map[string]any{"options": map[string]any{"temperature": 0.5}},
		Artifacts: []extensions.ArtifactRef{{TypeRef: extensions.Ref{Kind: extensions.ArtifactKind, ID: "fixture", ContractVersion: 1}, Owner: extensions.ArtifactOwner{Domain: "provider"}, ReplayScope: extensions.ArtifactReplayRequest, Sensitivity: extensions.ArtifactPrivate, MediaType: "application/json", Value: json.RawMessage(`{"value":"original"}`), SizeBytes: int64(len(`{"value":"original"}`))}},
	}
	branch := CloneRequest(request)
	branch.Messages[0].Content.([]any)[0].(map[string]any)["text"] = "branch"
	branch.Messages[0].ToolCalls[0].Arguments.(map[string]any)["nested"].([]any)[0] = "branch"
	branch.Messages[0].ToolCalls[0].ProviderData[2] = 'X'
	branch.Prompt.Layers[0].Parts[0].Metadata["tags"].([]any)[0] = "branch"
	branch.Raw["options"].(map[string]any)["temperature"] = 0.9
	branch.Artifacts[0].Value[10] = 'X'
	branch.Artifacts[0].Owner.Domain = "mutated"

	if request.Messages[0].Content.([]any)[0].(map[string]any)["text"] != "original" || request.Messages[0].ToolCalls[0].Arguments.(map[string]any)["nested"].([]any)[0] != "original" || string(request.Messages[0].ToolCalls[0].ProviderData) != `{"sig":"x"}` || request.Prompt.Layers[0].Parts[0].Metadata["tags"].([]any)[0] != "original" || request.Raw["options"].(map[string]any)["temperature"] != 0.5 || string(request.Artifacts[0].Value) != `{"value":"original"}` || request.Artifacts[0].Owner.Domain != "provider" {
		t.Fatalf("branch transform mutated the shared invocation: %#v", request)
	}
}
