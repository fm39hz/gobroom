package kernel

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

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

type SourceFidelity string

const (
	FidelityExact      SourceFidelity = "exact"
	FidelityAlias      SourceFidelity = "alias"
	FidelityCompatible SourceFidelity = "compatible"
	FidelityDynamic    SourceFidelity = "dynamic"
	FidelityUnknown    SourceFidelity = "unknown"
)

type Evidence struct {
	Source     string    `json:"source"`
	Confidence float64   `json:"confidence,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	Note       string    `json:"note,omitempty"`
}

type PhysicalIdentity struct {
	CanonicalName string `json:"canonicalName"`
	Family        string `json:"family,omitempty"`
	Revision      string `json:"revision,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
}

type ProfileProjection struct {
	Declared          CapabilityProfile   `json:"declared"`
	Guaranteed        CapabilityProfile   `json:"guaranteed"`
	Available         CapabilityProfile   `json:"available"`
	Limits            TokenLimits         `json:"guaranteedLimits"`
	AvailableLimits   TokenLimits         `json:"availableLimits"`
	CapabilitySources map[string][]string `json:"capabilitySources,omitempty"`
	LimitSources      map[string][]string `json:"limitSources,omitempty"`
}

// ProjectProfiles derives semantic projections from active source routes. A
// guaranteed capability is present on every route; an available capability is
// present on at least one route. Limits are conservative for guaranteed use
// and optimistic only for the explicitly available view.
func ProjectProfiles(routes []Route) ProfileProjection {
	projection := ProfileProjection{Declared: CapabilityProfile{}, Guaranteed: CapabilityProfile{}, Available: CapabilityProfile{}, CapabilitySources: map[string][]string{}, LimitSources: map[string][]string{}}
	if len(routes) == 0 {
		return projection
	}
	for _, route := range routes {
		for name, capability := range route.Profile {
			projection.CapabilitySources[name] = append(projection.CapabilitySources[name], route.ID)
			if existing, ok := projection.Declared[name]; !ok || capabilityRank(capability.State) > capabilityRank(existing.State) {
				projection.Declared[name] = capability
			}
		}
	}
	names := make(map[string]bool)
	for name := range projection.Declared {
		names[name] = true
	}
	for name := range names {
		available := SupportUnsupported
		guaranteed := SupportUnsupported
		count := 0
		for _, route := range routes {
			capability, ok := route.Profile[name]
			state := SupportUnknown
			if ok && capability.State != "" {
				state = capability.State
			}
			if capabilityRank(state) > capabilityRank(available) {
				available = state
			}
			if count == 0 {
				guaranteed = state
			} else if guaranteed != state {
				guaranteed = SupportUnknown
			}
			count++
		}
		projection.Available[name] = Capability{State: available}
		projection.Guaranteed[name] = Capability{State: guaranteed}
	}
	projection.Limits, projection.AvailableLimits = projectLimits(routes)
	for _, route := range routes {
		if route.Limits.MaxInputTokens > 0 {
			projection.LimitSources["maxInputTokens"] = append(projection.LimitSources["maxInputTokens"], route.ID)
		}
		if route.Limits.MaxOutputTokens > 0 {
			projection.LimitSources["maxOutputTokens"] = append(projection.LimitSources["maxOutputTokens"], route.ID)
		}
		if route.Limits.MaxTotalTokens > 0 {
			projection.LimitSources["maxTotalTokens"] = append(projection.LimitSources["maxTotalTokens"], route.ID)
		}
	}
	return projection
}

func capabilityRank(state SupportState) int {
	switch state {
	case SupportNative:
		return 5
	case SupportEmulated:
		return 4
	case SupportConditional:
		return 3
	case SupportUnsupported:
		return 1
	default:
		return 0
	}
}

func projectLimits(routes []Route) (TokenLimits, TokenLimits) {
	guaranteed, available := TokenLimits{}, TokenLimits{}
	inputMin, outputMin, totalMin := int64(0), int64(0), int64(0)
	inputMax, outputMax, totalMax := int64(0), int64(0), int64(0)
	for _, route := range routes {
		limits := route.Limits
		if limits.MaxInputTokens > 0 {
			if inputMin == 0 || limits.MaxInputTokens < inputMin {
				inputMin = limits.MaxInputTokens
			}
			if limits.MaxInputTokens > inputMax {
				inputMax = limits.MaxInputTokens
			}
		}
		if limits.MaxOutputTokens > 0 {
			if outputMin == 0 || limits.MaxOutputTokens < outputMin {
				outputMin = limits.MaxOutputTokens
			}
			if limits.MaxOutputTokens > outputMax {
				outputMax = limits.MaxOutputTokens
			}
		}
		if limits.MaxTotalTokens > 0 {
			if totalMin == 0 || limits.MaxTotalTokens < totalMin {
				totalMin = limits.MaxTotalTokens
			}
			if limits.MaxTotalTokens > totalMax {
				totalMax = limits.MaxTotalTokens
			}
		}
	}
	guaranteed.MaxInputTokens, guaranteed.MaxOutputTokens, guaranteed.MaxTotalTokens = inputMin, outputMin, totalMin
	available.MaxInputTokens, available.MaxOutputTokens, available.MaxTotalTokens = inputMax, outputMax, totalMax
	return guaranteed, available
}

func CapabilityNames(profile CapabilityProfile) []string {
	result := make([]string, 0, len(profile))
	for name := range profile {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
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
