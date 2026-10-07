package kernel

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fm39hz/gobroom/internal/normalize"
)

// FacetDisposition describes what one stage does to one semantic facet.
// Unknown is deliberately distinct from unsupported: both fail closed for a
// required facet, but unknown also identifies a missing contract declaration.
type FacetDisposition string

const (
	FacetPreserved   FacetDisposition = "preserved"
	FacetTranslated  FacetDisposition = "translated"
	FacetDegraded    FacetDisposition = "degraded"
	FacetUnsupported FacetDisposition = "unsupported"
	FacetUnknown     FacetDisposition = "unknown"
)

func IsResponseCompatibilityFacet(facet string) bool {
	return facet == FacetWireResponse || strings.HasPrefix(facet, "response.event.")
}

const (
	FacetWireRequest             = "wire.request"
	FacetWireResponse            = "wire.response"
	FacetPromptLayers            = "prompt.layers"
	FacetToolDefinitions         = "tools.definitions"
	FacetToolHistory             = "tools.history"
	FacetReasoningIntent         = "reasoning.intent"
	FacetContinuity              = "continuity.previous_response"
	FacetVisionInput             = "input.vision"
	FacetAudioInput              = "input.audio"
	FacetVideoInput              = "input.video"
	FacetDocumentInput           = "input.document"
	FacetGenerationOptions       = "generation.options"
	FacetOperationArtifactPrefix = "operation.artifact."
)

func OperationArtifactFacet(role string) string { return FacetOperationArtifactPrefix + role }

func IsOperationArtifactFacet(facet string) bool {
	return strings.HasPrefix(facet, FacetOperationArtifactPrefix)
}

// FacetMapping is a stage-local declaration. Mappings are composed in runtime
// order; a downstream stage must accept the output semantics of its upstream.
type FacetMapping struct {
	Facet       string
	Paths       []string
	Disposition FacetDisposition
	LossIDs     []string
	Reason      string
}

type CompatibilityPolicy struct {
	RequiredFacets []string
	AllowedLosses  []string
	DeniedLosses   []string
}

// CompatibilityPlan is immutable evidence for admitting one adapter on one
// request. It records facet-level reasoning instead of collapsing it to a
// format boolean.
type CompatibilityPlan struct {
	Supported bool
	Fidelity  CompatibilityFidelity
	Mappings  []FacetMapping
	Losses    []string
	Reason    string
}

// ComposeCompatibilityPlan combines stage reports in execution order. A
// missing report for a required facet is unknown and therefore rejected.
func ComposeCompatibilityPlan(reports [][]FacetMapping, policy CompatibilityPolicy) CompatibilityPlan {
	byFacet := make(map[string][]FacetMapping)
	for _, report := range reports {
		for _, mapping := range report {
			if mapping.Facet == "" || !validFacetDisposition(mapping.Disposition) {
				return unsupportedPlan("compatibility stage returned an invalid facet mapping")
			}
			byFacet[mapping.Facet] = append(byFacet[mapping.Facet], cloneFacetMapping(mapping))
		}
	}

	required := uniqueSorted(policy.RequiredFacets)
	requiredSet := stringSet(required)
	all := make(map[string]bool, len(byFacet)+len(required))
	for facet := range byFacet {
		all[facet] = true
	}
	for _, facet := range required {
		all[facet] = true
	}
	facets := make([]string, 0, len(all))
	for facet := range all {
		facets = append(facets, facet)
	}
	sort.Strings(facets)

	allowed := stringSet(policy.AllowedLosses)
	denied := stringSet(policy.DeniedLosses)
	plan := CompatibilityPlan{Supported: true, Fidelity: FidelityNative}
	for _, facet := range facets {
		stages := byFacet[facet]
		if len(stages) == 0 {
			stages = []FacetMapping{{Facet: facet, Disposition: FacetUnknown, Reason: "no extension declared support for this required facet"}}
		}
		combined := FacetMapping{Facet: facet, Disposition: FacetPreserved}
		for _, stage := range stages {
			combined.Paths = appendUnique(combined.Paths, stage.Paths...)
			combined.LossIDs = appendUnique(combined.LossIDs, stage.LossIDs...)
			if stage.Reason != "" {
				combined.Reason = stage.Reason
			}
			switch stage.Disposition {
			case FacetUnsupported, FacetUnknown:
				combined.Disposition = stage.Disposition
			case FacetDegraded:
				if combined.Disposition != FacetUnsupported && combined.Disposition != FacetUnknown {
					combined.Disposition = FacetDegraded
				}
			case FacetTranslated:
				if combined.Disposition == FacetPreserved {
					combined.Disposition = FacetTranslated
				}
			}
		}
		if len(combined.LossIDs) > 0 && combined.Disposition != FacetUnknown && combined.Disposition != FacetUnsupported {
			combined.Disposition = FacetDegraded
		}
		if combined.Disposition == FacetDegraded && len(combined.LossIDs) == 0 {
			return unsupportedPlan(fmt.Sprintf("facet %q reports degradation without a named loss", facet))
		}
		for _, lossID := range combined.LossIDs {
			if denied[lossID] || !allowed[lossID] {
				combined.Disposition = FacetUnsupported
				combined.Reason = fmt.Sprintf("loss %q is not permitted by compatibility policy", lossID)
			}
		}
		if (requiredSet[facet] || len(combined.LossIDs) > 0) && (combined.Disposition == FacetUnknown || combined.Disposition == FacetUnsupported) {
			plan.Supported = false
			if plan.Reason == "" {
				plan.Reason = combined.Reason
				if plan.Reason == "" {
					plan.Reason = fmt.Sprintf("facet %q is %s", facet, combined.Disposition)
				}
			}
		}
		if combined.Disposition == FacetDegraded {
			plan.Fidelity = FidelityLossy
			plan.Losses = appendUnique(plan.Losses, combined.LossIDs...)
		} else if combined.Disposition == FacetTranslated && plan.Fidelity == FidelityNative {
			plan.Fidelity = FidelityTranslated
		}
		plan.Mappings = append(plan.Mappings, combined)
	}
	if !plan.Supported {
		plan.Fidelity = FidelityUnsupported
	}
	return plan
}

// RequiredRequestFacets extracts semantics actually present in the invocation.
// Capability constraints remain separately checked by the feature registry;
// this list asks each codec stage whether it can carry the requested meaning.
func RequiredRequestFacets(request NormalizedRequest) []string {
	required := []string{FacetWireRequest}
	if len(request.Prompt.Layers) > 0 {
		required = append(required, FacetPromptLayers)
	}
	if len(request.Tools) > 0 {
		required = append(required, FacetToolDefinitions)
	}
	if requestHasToolHistory(request.Messages) {
		required = append(required, FacetToolHistory)
	}
	if request.Thinking.Mode != "" && request.Thinking.Mode != "inherit" || request.Thinking.Effort != "" || request.Thinking.BudgetTokens > 0 {
		required = append(required, FacetReasoningIntent)
	}
	if request.Continuity.PreviousResponse != "" || request.Continuity.ResponseID != "" || len(request.Continuity.EncryptedContent) > 0 {
		required = append(required, FacetContinuity)
	}
	if request.Modalities.Vision {
		required = append(required, FacetVisionInput)
	}
	if request.Modalities.AudioInput {
		required = append(required, FacetAudioInput)
	}
	if request.Modalities.VideoInput {
		required = append(required, FacetVideoInput)
	}
	if request.Modalities.PDF {
		required = append(required, FacetDocumentInput)
	}
	for _, artifact := range request.Artifacts {
		if artifact.Role != "" {
			required = append(required, OperationArtifactFacet(artifact.Role))
		}
	}
	if hasGenerationOptions(request.Raw) {
		required = append(required, FacetGenerationOptions)
	}
	return uniqueSorted(required)
}

// RequiredResponseEvents describes event families the selected client renderer
// must be able to encode if the provider emits them for this invocation.
func RequiredResponseEvents(request NormalizedRequest) []ResponseEventKind {
	events := []ResponseEventKind{EventTextDelta, EventResponseComplete}
	if len(request.Tools) > 0 {
		events = append(events, EventToolCallDelta)
	}
	if request.Thinking.Mode != "" && request.Thinking.Mode != "inherit" || request.Thinking.Effort != "" || request.Thinking.BudgetTokens > 0 {
		events = append(events, EventThinkingDelta)
	}
	return uniqueEventKinds(events)
}

func ResponseEventFacet(event ResponseEventKind) string {
	return "response.event." + string(event)
}

func responseEventMappings(events []ResponseEventKind) []FacetMapping {
	result := make([]FacetMapping, 0, len(events))
	for _, event := range uniqueEventKinds(events) {
		result = append(result, FacetMapping{
			Facet: ResponseEventFacet(event), Paths: []string{"upstream.response.events"}, Disposition: FacetPreserved,
		})
	}
	return result
}

func uniqueEventKinds(events []ResponseEventKind) []ResponseEventKind {
	seen := make(map[ResponseEventKind]bool, len(events))
	result := make([]ResponseEventKind, 0, len(events))
	for _, event := range events {
		if event != "" && !seen[event] {
			seen[event] = true
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func hasGenerationOptions(raw map[string]any) bool {
	structural := map[string]bool{
		"model": true, "stream": true, "messages": true, "input": true, "contents": true,
		"tools": true, "reasoning_effort": true, "thinking": true, "reasoning": true,
		"conversation_id": true, "response_id": true, "previous_response_id": true,
		"operationPayload": true,
	}
	for key := range raw {
		if !structural[key] {
			return true
		}
	}
	return false
}

func requestHasToolHistory(messages []normalize.Message) bool {
	for _, message := range messages {
		if len(message.ToolCalls) > 0 || message.Role == "tool" || message.Role == "function" {
			return true
		}
	}
	return false
}

func validFacetDisposition(value FacetDisposition) bool {
	switch value {
	case FacetPreserved, FacetTranslated, FacetDegraded, FacetUnsupported, FacetUnknown:
		return true
	default:
		return false
	}
}

func unsupportedPlan(reason string) CompatibilityPlan {
	return CompatibilityPlan{Fidelity: FidelityUnsupported, Reason: reason}
}

func cloneFacetMapping(mapping FacetMapping) FacetMapping {
	mapping.Paths = append([]string(nil), mapping.Paths...)
	mapping.LossIDs = append([]string(nil), mapping.LossIDs...)
	return mapping
}

func uniqueSorted(values []string) []string {
	set := stringSet(values)
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func appendUnique(target []string, values ...string) []string {
	seen := make(map[string]bool, len(target)+len(values))
	for _, value := range target {
		seen[value] = true
	}
	for _, value := range values {
		if value != "" && !seen[value] {
			target = append(target, value)
			seen[value] = true
		}
	}
	return target
}
