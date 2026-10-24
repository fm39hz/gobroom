package gemini

import (
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/jsonvalue"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func geminiResponsesEnvelopeRepresentable(request kernel.NormalizedRequest) bool {
	allowed := map[string]bool{
		"model": true, "stream": true, "input": true, "instructions": true,
		"tools": true, "tool_choice": true, "parallel_tool_calls": true,
		"max_output_tokens": true, "temperature": true, "top_p": true, "reasoning": true,
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
	if parallel, exists := request.Raw["parallel_tool_calls"]; exists {
		value, ok := parallel.(bool)
		if !ok || !value {
			return false
		}
	}
	if rawReasoning, exists := request.Raw["reasoning"]; exists {
		reasoning, ok := rawReasoning.(map[string]any)
		if !ok || !responsesReasoningKeysOnly(reasoning, "effort", "max_output_tokens") {
			return false
		}
		if effort, hasEffort := reasoning["effort"]; hasEffort {
			if _, ok := effort.(string); !ok || reasoning["max_output_tokens"] != nil {
				return false
			}
		}
		if rawBudget, hasBudget := reasoning["max_output_tokens"]; hasBudget {
			if value, ok := jsonvalue.Integer(rawBudget); !ok || value <= 0 {
				return false
			}
		}
	}
	if rawChoice, exists := request.Raw["tool_choice"]; exists && rawChoice != nil && !request.ToolChoice.Set {
		return false
	}
	if rawChoice, exists := request.Raw["tool_choice"]; exists && rawChoice != nil && !request.ToolChoice.Set {
		return false
	}
	return true
}

func responsesReasoningKeysOnly(value map[string]any, allowed ...string) bool {
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

func geminiResponsesToolChoiceRepresentable(choice normalize.ToolChoice) bool {
	if !choice.Set {
		return true
	}
	if len(choice.Metadata) > 0 || choice.DisableParallelTools {
		return false
	}
	switch choice.Mode {
	case "auto", "any", "none":
		return choice.Name == ""
	case "tool":
		return strings.TrimSpace(choice.Name) != ""
	default:
		return false
	}
}

func geminiToolModeForNormalizedChoice(choice normalize.ToolChoice) (*geminiFunctionCallingConfig, error) {
	if !choice.Set {
		return nil, nil
	}
	if len(choice.Metadata) > 0 || choice.DisableParallelTools {
		return nil, fmt.Errorf("Gemini cannot preserve Responses tool-choice metadata or disabled parallel calls")
	}
	switch choice.Mode {
	case "none":
		return &geminiFunctionCallingConfig{Mode: "NONE"}, nil
	case "auto":
		return &geminiFunctionCallingConfig{Mode: "AUTO"}, nil
	case "any":
		return &geminiFunctionCallingConfig{Mode: "ANY"}, nil
	case "tool":
		if strings.TrimSpace(choice.Name) == "" {
			return nil, fmt.Errorf("named Gemini function choice is missing its tool name")
		}
		return &geminiFunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: []string{choice.Name}}, nil
	default:
		return nil, fmt.Errorf("unsupported Responses function choice %q", choice.Mode)
	}
}

func geminiResponsesGenerationOptionsRepresentable(request kernel.NormalizedRequest) bool {
	options := request.Generation
	if len(options.Unsupported) > 0 || len(options.StopSequences) > 0 {
		return false
	}
	if options.MaxOutputTokens != nil && *options.MaxOutputTokens <= 0 {
		return false
	}
	if options.Temperature != nil && (*options.Temperature < 0 || *options.Temperature > 2) {
		return false
	}
	return options.TopP == nil || *options.TopP >= 0 && *options.TopP <= 1
}

func geminiResponsesVisionRepresentable(request kernel.NormalizedRequest) bool {
	if !request.Modalities.Vision {
		return true
	}
	found := false
	for _, message := range request.Messages {
		parts, ok := message.Content.([]normalize.ContentPart)
		if !ok {
			continue
		}
		for _, part := range parts {
			if part.Type != "image" {
				continue
			}
			found = true
			if part.Detail != "" {
				return false
			}
			imageURL := part.URL
			if imageURL == "" && part.Data != "" && part.MediaType != "" {
				imageURL = "data:" + part.MediaType + ";base64," + part.Data
			}
			if !strings.HasPrefix(imageURL, "data:image/") || !strings.Contains(imageURL, ";base64,") {
				return false
			}
		}
	}
	return found
}
