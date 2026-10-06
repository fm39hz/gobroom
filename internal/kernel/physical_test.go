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

func TestTypedCapabilityProfileRejectsConditionalWithoutEvaluator(t *testing.T) {
	req := NormalizedRequest{SourceFormat: normalize.FormatOpenAIChat, Modalities: normalize.Modalities{Vision: true}}
	route := Route{Protocol: ProtocolOpenAIChat, Profile: CapabilityProfile{CapabilityVision: {State: SupportConditional}}}
	if ok, _ := Eligible(route, CompileRequirements(req)); ok {
		t.Fatal("conditional capability passed without evaluating constraints")
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
		{ID: "native", Profile: CapabilityProfile{"input.image": {State: SupportNative}}, Limits: TokenLimits{MaxInputTokens: 200000, MaxTotalTokens: 250000}},
		{ID: "unsupported", Profile: CapabilityProfile{"input.image": {State: SupportUnsupported}}, Limits: TokenLimits{MaxInputTokens: 100000, MaxTotalTokens: 128000}},
		{ID: "unknown", Profile: CapabilityProfile{}, Limits: TokenLimits{}},
	}
	declared := CapabilityProfile{CapabilityVision: {State: SupportConditional}}
	projection := ProjectProfiles(declared, routes)
	if projection.Declared[CapabilityVision].State != SupportConditional {
		t.Fatalf("declared=%#v", projection.Declared)
	}
	if projection.Available[CapabilityVision].State != SupportNative {
		t.Fatalf("available=%#v", projection.Available)
	}
	if projection.Guaranteed[CapabilityVision].State != SupportUnsupported {
		t.Fatalf("guaranteed=%#v", projection.Guaranteed)
	}
	if projection.Limits.MaxInputTokens != 100000 || projection.AvailableLimits.MaxInputTokens != 200000 {
		t.Fatalf("limits=%#v/%#v", projection.Limits, projection.AvailableLimits)
	}
	if len(projection.CapabilitySources[CapabilityVision]) != 2 || len(projection.LimitSources["maxInputTokens"]) != 2 {
		t.Fatalf("source explanations=%#v/%#v", projection.CapabilitySources, projection.LimitSources)
	}
}

func TestPhysicalSourceFidelityEligibilityRequiresEvidenceAndOptIn(t *testing.T) {
	node := ModelNode{Kind: ModelPhysical}
	if PhysicalSourceAllowed(node, MemberRef{Fidelity: FidelityUnknown}) {
		t.Fatal("unknown source should be excluded")
	}
	if PhysicalSourceAllowed(node, MemberRef{Fidelity: FidelityAlias}) {
		t.Fatal("alias without evidence should be excluded")
	}
	if !PhysicalSourceAllowed(node, MemberRef{Fidelity: FidelityAlias, Evidence: []Evidence{{Source: "user_assertion"}}}) {
		t.Fatal("evidence-backed alias should be eligible")
	}
	if PhysicalSourceAllowed(node, MemberRef{Fidelity: FidelityCompatible}) {
		t.Fatal("compatible source requires opt-in")
	}
	node.AllowCompatibleSources = true
	if !PhysicalSourceAllowed(node, MemberRef{Fidelity: FidelityCompatible}) {
		t.Fatal("compatible source opt-in ignored")
	}
}
