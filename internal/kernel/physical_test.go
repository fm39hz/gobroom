package kernel

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestTypedCapabilityProfileRejectsUnknownAndAcceptsNative(t *testing.T) {
	req := NormalizedRequest{SourceFormat: normalize.FormatOpenAIChat, Modalities: normalize.Modalities{Vision: true}}
	route := Route{Protocol: ProtocolOpenAIChat, Profile: CapabilityProfile{CapabilityVision: {State: SupportUnknown}}}
	if ok, _ := Eligible(route, CompileRequirements(req)); ok {
		t.Fatal("unknown capability must not satisfy hard eligibility")
	}
	route.Profile[CapabilityVision] = Capability{State: SupportNative, Formats: []string{"image/png"}}
	if ok, reason := Eligible(route, CompileRequirements(req)); !ok {
		t.Fatalf("native capability rejected: %s", reason)
	}
}

func TestCompileRequirementsCapturesToolsAndReasoning(t *testing.T) {
	req := NormalizedRequest{SourceFormat: normalize.FormatOpenAIResponses, Stream: true, Tools: []Tool{{Name: "lookup"}}, Thinking: normalize.ThinkingIntent{Mode: "level", Effort: "high"}}
	compiled := CompileRequirements(req)
	if compiled.Protocol != ProtocolOpenAIResponses || !compiled.Streaming || !compiled.Reasoning {
		t.Fatalf("requirements=%#v", compiled)
	}
	if len(compiled.Capabilities) != 1 || compiled.Capabilities[0] != CapabilityTools {
		t.Fatalf("requirements=%#v", compiled)
	}
}

func TestEligibleRejectsTokenBudgetOverflow(t *testing.T) {
	req := NormalizedRequest{SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"messages": "a", "max_tokens": float64(200)}}
	route := Route{Protocol: ProtocolOpenAIChat, Limits: TokenLimits{MaxOutputTokens: 100, MaxTotalTokens: 1000}}
	if ok, reason := Eligible(route, CompileRequirements(req)); ok || reason != "output exceeds route limit" {
		t.Fatalf("expected output limit rejection, ok=%v reason=%q", ok, reason)
	}
}

func TestProjectProfilesSeparatesGuaranteedAndAvailable(t *testing.T) {
	routes := []Route{
		{Profile: CapabilityProfile{"input.image": {State: SupportNative}}, Limits: TokenLimits{MaxInputTokens: 200000, MaxTotalTokens: 250000}},
		{Profile: CapabilityProfile{"input.image": {State: SupportUnsupported}}, Limits: TokenLimits{MaxInputTokens: 100000, MaxTotalTokens: 128000}},
	}
	projection := ProjectProfiles(routes)
	if projection.Available[CapabilityVision].State != SupportNative {
		t.Fatalf("available=%#v", projection.Available)
	}
	if projection.Guaranteed[CapabilityVision].State != SupportUnknown {
		t.Fatalf("guaranteed=%#v", projection.Guaranteed)
	}
	if projection.Limits.MaxInputTokens != 100000 || projection.AvailableLimits.MaxInputTokens != 200000 {
		t.Fatalf("limits=%#v/%#v", projection.Limits, projection.AvailableLimits)
	}
	if len(projection.CapabilitySources[CapabilityVision]) != 2 || len(projection.LimitSources["maxInputTokens"]) != 2 {
		t.Fatalf("source explanations=%#v/%#v", projection.CapabilitySources, projection.LimitSources)
	}
}
