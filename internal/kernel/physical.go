package kernel

import (
	"encoding/json"
	"fmt"

	"github.com/fm39hz/gobroom/internal/normalize"
)

// SupportState describes capability evidence. Unknown is deliberately not
// silently treated as supported or unsupported.
type SupportState string

const (
	SupportUnknown     SupportState = "unknown"
	SupportUnsupported SupportState = "unsupported"
	SupportNative      SupportState = "native"
	SupportEmulated    SupportState = "emulated"
	SupportConditional SupportState = "conditional"
)

type Capability struct {
	State    SupportState `json:"state"`
	Formats  []string     `json:"formats,omitempty"`
	MaxItems int          `json:"maxItems,omitempty"`
}

type CapabilityProfile map[string]Capability

type TokenLimits struct {
	MaxInputTokens  int64  `json:"maxInputTokens,omitempty"`
	MaxOutputTokens int64  `json:"maxOutputTokens,omitempty"`
	MaxTotalTokens  int64  `json:"maxTotalTokens,omitempty"`
	Tokenizer       string `json:"tokenizer,omitempty"`
	CountingMode    string `json:"countingMode,omitempty"`
}

const (
	CapabilityVision = "input.image"
	CapabilityAudio  = "input.audio"
	CapabilityVideo  = "input.video"
	CapabilityPDF    = "input.pdf"
	CapabilityTools  = "tools.function"
)

type RequestRequirements struct {
	Capabilities         []string
	Protocol             Protocol
	Streaming            bool
	Reasoning            bool
	EstimatedInputTokens int64
	ReservedOutputTokens int64
}

func CompileRequirements(req NormalizedRequest) RequestRequirements {
	result := RequestRequirements{Protocol: protocolForFormat(req.SourceFormat), Streaming: req.Stream, Reasoning: req.Thinking.Effort != "" || req.Thinking.Mode == "level" || req.Thinking.Mode == "budget", EstimatedInputTokens: estimateInputTokens(req), ReservedOutputTokens: requestedOutputTokens(req)}
	if req.Modalities.Vision {
		result.Capabilities = append(result.Capabilities, CapabilityVision)
	}
	if req.Modalities.AudioInput {
		result.Capabilities = append(result.Capabilities, CapabilityAudio)
	}
	if req.Modalities.VideoInput {
		result.Capabilities = append(result.Capabilities, CapabilityVideo)
	}
	if req.Modalities.PDF {
		result.Capabilities = append(result.Capabilities, CapabilityPDF)
	}
	if len(req.Tools) > 0 {
		result.Capabilities = append(result.Capabilities, CapabilityTools)
	}
	return result
}

func estimateInputTokens(req NormalizedRequest) int64 {
	if len(req.Raw) == 0 {
		return int64(len(req.Messages) * 16)
	}
	data, _ := json.Marshal(req.Raw)
	return int64((len(data) + 3) / 4)
}

func requestedOutputTokens(req NormalizedRequest) int64 {
	for _, key := range []string{"max_output_tokens", "max_tokens"} {
		if value, ok := req.Raw[key].(float64); ok && value > 0 {
			return int64(value)
		}
	}
	return 0
}

func protocolForFormat(format normalize.Format) Protocol {
	switch format {
	case "anthropic":
		return ProtocolAnthropic
	case "openai-responses":
		return ProtocolOpenAIResponses
	default:
		return ProtocolOpenAIChat
	}
}

// Eligible evaluates typed profile evidence. A typed profile is conservative:
// unknown cannot satisfy a hard request requirement. Routes without a typed
// profile use the existing explicit boolean catalog fields until profile
// discovery is wired into storage.
func Eligible(route Route, requirements RequestRequirements) (bool, string) {
	if route.Protocol != "" && requirements.Protocol != "" && route.Protocol != requirements.Protocol && !(requirements.Protocol == ProtocolOpenAIChat && route.Protocol == ProtocolAnthropic) {
		return false, fmt.Sprintf("protocol %s is not accepted", requirements.Protocol)
	}
	if len(route.Profile) > 0 {
		for _, capability := range requirements.Capabilities {
			item, ok := route.Profile[capability]
			if !ok || item.State == "" || item.State == SupportUnknown || item.State == SupportUnsupported {
				return false, capability + " is not proven supported"
			}
		}
	} else {
		checks := map[string]bool{CapabilityVision: route.Capabilities["vision"], CapabilityAudio: route.Capabilities["audio"], CapabilityVideo: route.Capabilities["video"], CapabilityPDF: route.Capabilities["pdf"]}
		for _, capability := range requirements.Capabilities {
			if capability == CapabilityTools {
				continue
			}
			if needed := checks[capability]; !needed {
				return false, capability + " is not declared"
			}
		}
	}
	limits := route.Limits
	if limits.MaxInputTokens > 0 && requirements.EstimatedInputTokens > limits.MaxInputTokens {
		return false, "input exceeds route limit"
	}
	if limits.MaxOutputTokens > 0 && requirements.ReservedOutputTokens > limits.MaxOutputTokens {
		return false, "output exceeds route limit"
	}
	if limits.MaxTotalTokens > 0 && requirements.EstimatedInputTokens+requirements.ReservedOutputTokens > limits.MaxTotalTokens {
		return false, "total tokens exceed route limit"
	}
	return true, ""
}
