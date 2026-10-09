package openai

import (
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// overlayNativeResponsesMutations applies only canonical facets explicitly
// changed by request transforms. Untouched raw fields remain byte-semantically
// lossless; unsupported semantic rewrites fail closed rather than being lost.
func overlayNativeResponsesMutations(body map[string]any, request kernel.NormalizedRequest) error {
	mutations := request.Mutations
	if mutations.Messages || mutations.Prompt || mutations.OperationPayload || mutations.Modalities {
		return fmt.Errorf("OpenAI Responses egress cannot yet safely project transformed input, prompt or modality facets")
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
