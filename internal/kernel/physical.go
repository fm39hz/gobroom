package kernel

import "fmt"

import "github.com/fm39hz/gobroom/internal/normalize"

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

const (
	CapabilityVision = "input.image"
	CapabilityAudio  = "input.audio"
	CapabilityVideo  = "input.video"
	CapabilityPDF    = "input.pdf"
	CapabilityTools  = "tools.function"
)

type RequestRequirements struct {
	Capabilities []string
	Protocol     Protocol
	Streaming    bool
	Reasoning    bool
}

func CompileRequirements(req NormalizedRequest) RequestRequirements {
	result := RequestRequirements{Protocol: protocolForFormat(req.SourceFormat), Streaming: req.Stream, Reasoning: req.Thinking.Effort != "" || req.Thinking.Mode == "level" || req.Thinking.Mode == "budget"}
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
		return true, ""
	}
	checks := map[string]bool{CapabilityVision: route.Capabilities["vision"], CapabilityAudio: route.Capabilities["audio"], CapabilityVideo: route.Capabilities["video"], CapabilityPDF: route.Capabilities["pdf"]}
	for _, capability := range requirements.Capabilities {
		if capability == CapabilityTools {
			continue
		}
		if needed := checks[capability]; !needed {
			return false, capability + " is not declared"
		}
	}
	return true, ""
}
