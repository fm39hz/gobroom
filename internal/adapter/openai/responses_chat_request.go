package openai

import (
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func chatProviderResponsesIngressFacetRepresentable(request kernel.NormalizedRequest, facet string) bool {
	if hasUnsupportedFacet(request, facet) || !openAIResponsesChatEnvelopeRepresentable(request) {
		return false
	}
	switch facet {
	case kernel.FacetWireRequest:
		return !request.Mutations.OperationPayload && !request.Mutations.Modalities
	case kernel.FacetPromptLayers:
		for _, layer := range request.Prompt.Layers {
			if layer.Role != "" && layer.Role != "system" && layer.Role != "developer" {
				return false
			}
			for _, part := range layer.Parts {
				if part.Type != "text" || part.Metadata["cache_control"] != nil {
					return false
				}
			}
		}
		return true
	case kernel.FacetToolDefinitions:
		return openAIAnthropicToolsRepresentable(request)
	case kernel.FacetToolHistory:
		return openAIAnthropicHistoryRepresentable(request)
	case kernel.FacetToolChoice:
		return len(request.ToolChoice.Metadata) == 0 && openAIAnthropicToolChoiceRepresentable(request.ToolChoice) && openAIAnthropicToolChoiceMatchesTools(request)
	case kernel.FacetReasoningIntent:
		return chatProviderResponsesIngressReasoningRepresentable(request.Thinking)
	case kernel.FacetReasoningBudget:
		return false
	case kernel.FacetContinuity:
		return false
	case kernel.FacetVisionInput:
		return openAIAnthropicContentRepresentable(request)
	case kernel.FacetGenerationOptions:
		return openAIAnthropicGenerationRepresentable(request.Generation)
	default:
		return false
	}
}

func chatProviderResponsesIngressReasoningRepresentable(intent normalize.ThinkingIntent) bool {
	if intent.Mode == "" || intent.Mode == "inherit" {
		return intent.Effort == "" && intent.BudgetTokens == 0
	}
	return openAIChatReasoningMutationRepresentable(intent)
}

func openAIResponsesChatEnvelopeRepresentable(request kernel.NormalizedRequest) bool {
	allowed := map[string]bool{
		"model": true, "stream": true, "input": true, "instructions": true,
		"tools": true, "tool_choice": true, "parallel_tool_calls": true,
		"max_output_tokens": true, "max_completion_tokens": true, "max_tokens": true,
		"temperature": true, "top_p": true, "reasoning": true,
		"previous_response_id": true, "response_id": true,
	}
	for key := range request.Raw {
		if !allowed[key] {
			return false
		}
	}
	for key := range request.Extensions {
		if !allowed[key] {
			return false
		}
	}
	if request.Continuity.PreviousResponse != "" || request.Continuity.ResponseID != "" || len(request.Continuity.EncryptedContent) > 0 {
		return false
	}
	if rawInstructions, exists := request.Raw["instructions"]; exists {
		if _, ok := rawInstructions.(string); !ok {
			return false
		}
	}
	if rawStream, exists := request.Raw["stream"]; exists {
		if _, ok := rawStream.(bool); !ok {
			return false
		}
	}
	if rawParallel, exists := request.Raw["parallel_tool_calls"]; exists {
		if _, ok := rawParallel.(bool); !ok {
			return false
		}
	}
	if rawChoice, exists := request.Raw["tool_choice"]; exists && !responsesChatToolChoiceEnvelopeRepresentable(rawChoice) {
		return false
	}
	if rawReasoning, exists := request.Raw["reasoning"]; exists {
		reasoning, ok := rawReasoning.(map[string]any)
		if !ok || !openAIResponsesOnlyKeys(reasoning, "effort") {
			return false
		}
	}
	return true
}

func responsesChatToolChoiceEnvelopeRepresentable(value any) bool {
	if mode, ok := value.(string); ok {
		return mode == "auto" || mode == "none" || mode == "required"
	}
	choice, ok := value.(map[string]any)
	if !ok || !openAIResponsesOnlyKeys(choice, "type", "name") {
		return false
	}
	switch stringValue(choice["type"]) {
	case "auto", "none", "required":
		return choice["name"] == nil
	case "function":
		return strings.TrimSpace(stringValue(choice["name"])) != ""
	default:
		return false
	}
}

func openAIResponsesOnlyKeys(value map[string]any, allowed ...string) bool {
	for key := range value {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
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

func encodeOpenAIResponsesRequestAsOpenAIChat(request kernel.NormalizedRequest, model string) (map[string]any, error) {
	if len(request.UnsupportedFacets) > 0 || !openAIResponsesChatEnvelopeRepresentable(request) ||
		!chatProviderResponsesIngressFacetRepresentable(request, kernel.FacetPromptLayers) ||
		!chatProviderResponsesIngressFacetRepresentable(request, kernel.FacetToolDefinitions) ||
		!chatProviderResponsesIngressFacetRepresentable(request, kernel.FacetToolHistory) ||
		!chatProviderResponsesIngressFacetRepresentable(request, kernel.FacetToolChoice) ||
		!chatProviderResponsesIngressFacetRepresentable(request, kernel.FacetGenerationOptions) ||
		!chatProviderResponsesIngressFacetRepresentable(request, kernel.FacetVisionInput) ||
		!chatProviderResponsesIngressReasoningRepresentable(request.Thinking) {
		return nil, fmt.Errorf("OpenAI Responses request contains semantics that OpenAI Chat cannot represent")
	}
	body, err := encodeTypedRequestAsOpenAIChat(request, model)
	if err != nil {
		return nil, err
	}
	if request.Thinking.Mode == "level" {
		body["reasoning_effort"] = request.Thinking.Effort
	}
	return body, nil
}
