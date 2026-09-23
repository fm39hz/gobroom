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
