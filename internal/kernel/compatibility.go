package kernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fm39hz/gobroom/internal/extensions"
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
	FacetToolChoice              = "tools.choice"
	FacetReasoningIntent         = "reasoning.intent"
	FacetReasoningSignature      = "reasoning.signature"
	FacetContinuity              = "continuity.previous_response"
	FacetOpaqueContent           = "content.opaque"
	FacetToolResultStatus        = "tools.result_status"
	FacetToolBlockOrder          = "tools.block_order"
	FacetClientMetadata          = "client.metadata"
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
	Facet       string           `json:"facet"`
	Paths       []string         `json:"paths,omitempty"`
	Disposition FacetDisposition `json:"disposition"`
	Losses      []LossRecord     `json:"losses,omitempty"`
	Reason      string           `json:"reason,omitempty"`
}

// LossRecord describes an explicit value change rather than only naming it.
// Requested/effective are JSON values owned by the codec that declares the
// degradation; policy sources are added by the model-path planner.
type LossRecord struct {
	ID            string   `json:"id"`
	Requested     any      `json:"requested"`
	Effective     any      `json:"effective"`
	SemanticPaths []string `json:"semanticPaths"`
	PolicySources []string `json:"policySources,omitempty"`
}

type CompatibilityPolicy struct {
	RequiredFacets []string
	AllowedLosses  []string
	DeniedLosses   []string
	LossSources    map[string][]string
	LossCeiling    LossPolicyCeiling
}

// LossPolicy is attached to a node in the model graph. Grants accumulate down
// the selected path; denials accumulate as hard ceilings and always win.
type LossPolicy struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// LossPolicyCeiling is a daemon-level limit on model-node grants. Deny is
// always enforced; AllowOnly restricts grants to the listed IDs, including an
// empty list which means that no degradation may be enabled by model policy.
type LossPolicyCeiling struct {
	AllowOnly bool     `json:"allowOnly,omitempty"`
	Allow     []string `json:"allow,omitempty"`
	Deny      []string `json:"deny,omitempty"`
}

func (ceiling LossPolicyCeiling) IsEmpty() bool {
	return !ceiling.AllowOnly && len(ceiling.Allow) == 0 && len(ceiling.Deny) == 0
}

func ValidateLossPolicyCeiling(ceiling LossPolicyCeiling) error {
	if !ceiling.AllowOnly && len(ceiling.Allow) > 0 {
		return fmt.Errorf("loss ceiling allow list requires allowOnly=true")
	}
	return ValidateLossPolicy(LossPolicy{Allow: ceiling.Allow, Deny: ceiling.Deny})
}

func AddLossPolicy(policy CompatibilityPolicy, source string, node LossPolicy) CompatibilityPolicy {
	policy.AllowedLosses = appendUnique(policy.AllowedLosses, node.Allow...)
	policy.DeniedLosses = appendUnique(policy.DeniedLosses, node.Deny...)
	if policy.LossSources == nil {
		policy.LossSources = make(map[string][]string)
	}
	for _, id := range node.Allow {
		policy.LossSources[id] = appendUnique(policy.LossSources[id], source)
	}
	policy.AllowedLosses = uniqueSorted(policy.AllowedLosses)
	policy.DeniedLosses = uniqueSorted(policy.DeniedLosses)
	for id := range policy.LossSources {
		policy.LossSources[id] = uniqueSorted(policy.LossSources[id])
	}
	return policy
}

func ValidateLossPolicy(policy LossPolicy) error {
	for _, item := range []struct {
		field string
		ids   []string
	}{{field: "allow", ids: policy.Allow}, {field: "deny", ids: policy.Deny}} {
		seen := make(map[string]bool, len(item.ids))
		for _, id := range item.ids {
			if id == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, " \t\r\n") {
				return fmt.Errorf("loss policy %s contains invalid loss ID %q", item.field, id)
			}
			if seen[id] {
				return fmt.Errorf("loss policy %s contains duplicate loss ID %q", item.field, id)
			}
			seen[id] = true
		}
	}
	return nil
}

// CompatibilityPlan is immutable evidence for admitting one adapter on one
// request. It records facet-level reasoning instead of collapsing it to a
// format boolean.
type CompatibilityPlan struct {
	Supported              bool
	Fidelity               CompatibilityFidelity
	Mappings               []FacetMapping
	Losses                 []LossRecord
	RequestTransformSteps  []RequestTransformPlanStep
	ResponseTransformSteps []ResponseTransformPlanStep
	ArtifactTransfers      []ArtifactTransfer
	Reason                 string
}

// ArtifactTransfer is the request-local, payload-free summary of one
// operation-authorized artifact handoff to an upstream provider definition.
type ArtifactTransfer struct {
	TypeRef        extensions.Ref                 `json:"typeRef"`
	Role           string                         `json:"role,omitempty"`
	OperationRef   extensions.Ref                 `json:"operationRef"`
	SourceDomain   string                         `json:"sourceDomain"`
	TargetProvider extensions.Ref                 `json:"targetProvider"`
	ReplayScope    extensions.ArtifactReplayScope `json:"replayScope"`
	Sensitivity    extensions.ArtifactSensitivity `json:"sensitivity"`
	MediaType      string                         `json:"mediaType"`
	SizeBytes      int64                          `json:"sizeBytes"`
	BodyLease      bool                           `json:"bodyLease,omitempty"`
}

// CompatibilityPlanSummary is the payload-free explanation persisted with a
// completed usage event. It intentionally excludes request content and loss
// values; loss evidence is recorded separately.
type CompatibilityPlanSummary struct {
	Supported              bool                          `json:"supported"`
	Fidelity               CompatibilityFidelity         `json:"fidelity"`
	Mappings               []CompatibilityMappingSummary `json:"mappings,omitempty"`
	RequestTransformSteps  []RequestTransformPlanStep    `json:"requestTransformSteps,omitempty"`
	ResponseTransformSteps []ResponseTransformPlanStep   `json:"responseTransformSteps,omitempty"`
	ArtifactTransfers      []ArtifactTransfer            `json:"artifactTransfers,omitempty"`
	Reason                 string                        `json:"reason,omitempty"`
}

type CompatibilityMappingSummary struct {
	Facet       string           `json:"facet"`
	Paths       []string         `json:"paths,omitempty"`
	Disposition FacetDisposition `json:"disposition"`
	Reason      string           `json:"reason,omitempty"`
}

func SummarizeCompatibilityPlan(plan CompatibilityPlan) CompatibilityPlanSummary {
	summary := CompatibilityPlanSummary{
		Supported: plan.Supported, Fidelity: plan.Fidelity,
		RequestTransformSteps:  cloneRequestTransformSteps(plan.RequestTransformSteps),
		ResponseTransformSteps: cloneResponseTransformSteps(plan.ResponseTransformSteps),
		ArtifactTransfers:      append([]ArtifactTransfer(nil), plan.ArtifactTransfers...),
		Reason:                 plan.Reason,
	}
	if len(plan.Mappings) > 0 {
		summary.Mappings = make([]CompatibilityMappingSummary, 0, len(plan.Mappings))
		for _, mapping := range plan.Mappings {
			summary.Mappings = append(summary.Mappings, CompatibilityMappingSummary{
				Facet: mapping.Facet, Paths: append([]string(nil), mapping.Paths...),
				Disposition: mapping.Disposition, Reason: mapping.Reason,
			})
		}
	}
	return summary
}

func PlanArtifactTransfers(request normalize.Request, operationRef extensions.Ref, recipient extensions.Ref) []ArtifactTransfer {
	result := make([]ArtifactTransfer, 0, len(request.Artifacts))
	for _, artifact := range request.Artifacts {
		result = append(result, ArtifactTransfer{
			TypeRef: artifact.TypeRef, Role: artifact.Role, OperationRef: operationRef,
			SourceDomain: artifact.Owner.Domain, TargetProvider: recipient,
			ReplayScope: artifact.ReplayScope, Sensitivity: artifact.Sensitivity,
			MediaType: artifact.MediaType, SizeBytes: artifact.SizeBytes, BodyLease: artifact.Body != nil,
		})
	}
	return result
}

func cloneCompatibilityPlanContext(plan CompatibilityPlan, input CompatibilityContext) CompatibilityPlan {
	plan.RequestTransformSteps = cloneRequestTransformSteps(input.RequestTransformSteps)
	plan.ResponseTransformSteps = cloneResponseTransformSteps(input.ResponseTransformSteps)
	plan.ArtifactTransfers = append([]ArtifactTransfer(nil), input.ArtifactTransfers...)
	return plan
}

func cloneRequestTransformSteps(steps []RequestTransformPlanStep) []RequestTransformPlanStep {
	result := append([]RequestTransformPlanStep(nil), steps...)
	for index := range result {
		result[index].Effects = append([]TransformEffect(nil), result[index].Effects...)
	}
	return result
}

func cloneResponseTransformSteps(steps []ResponseTransformPlanStep) []ResponseTransformPlanStep {
	result := append([]ResponseTransformPlanStep(nil), steps...)
	for index := range result {
		result[index].Effects = append([]ResponseTransformEffect(nil), result[index].Effects...)
	}
	return result
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
	ceilingAllowed := stringSet(policy.LossCeiling.Allow)
	ceilingDenied := stringSet(policy.LossCeiling.Deny)
	plan := CompatibilityPlan{Supported: true, Fidelity: FidelityNative}
	for _, facet := range facets {
		stages := byFacet[facet]
		if len(stages) == 0 {
			stages = []FacetMapping{{Facet: facet, Disposition: FacetUnknown, Reason: "no extension declared support for this required facet"}}
		}
		combined := FacetMapping{Facet: facet, Disposition: FacetPreserved}
		for _, stage := range stages {
			combined.Paths = appendUnique(combined.Paths, stage.Paths...)
			for _, loss := range stage.Losses {
				if err := validateLossRecord(loss); err != nil {
					return unsupportedPlan(fmt.Sprintf("facet %q has an invalid loss declaration: %v", facet, err))
				}
				// Policy provenance is planner-owned; a codec can declare the
				// degradation but cannot self-authorize the caller's grant.
				loss.PolicySources = nil
				var err error
				combined.Losses, err = mergeLossRecord(combined.Losses, loss)
				if err != nil {
					return unsupportedPlan(fmt.Sprintf("facet %q has conflicting loss declarations: %v", facet, err))
				}
			}
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
		if len(combined.Losses) > 0 && combined.Disposition != FacetUnknown && combined.Disposition != FacetUnsupported {
			combined.Disposition = FacetDegraded
		}
		if combined.Disposition == FacetDegraded && len(combined.Losses) == 0 {
			return unsupportedPlan(fmt.Sprintf("facet %q reports degradation without a named loss", facet))
		}
		for i := range combined.Losses {
			loss := &combined.Losses[i]
			if denied[loss.ID] || ceilingDenied[loss.ID] || !allowed[loss.ID] || (policy.LossCeiling.AllowOnly && !ceilingAllowed[loss.ID]) {
				combined.Disposition = FacetUnsupported
				combined.Reason = fmt.Sprintf("loss %q is not permitted by compatibility policy and server ceiling", loss.ID)
			} else {
				loss.PolicySources = appendUnique(loss.PolicySources, policy.LossSources[loss.ID]...)
			}
		}
		if (requiredSet[facet] || len(combined.Losses) > 0) && (combined.Disposition == FacetUnknown || combined.Disposition == FacetUnsupported) {
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
			for _, loss := range combined.Losses {
				var err error
				plan.Losses, err = mergeLossRecord(plan.Losses, loss)
				if err != nil {
					return unsupportedPlan(fmt.Sprintf("conflicting loss records for %q: %v", loss.ID, err))
				}
			}
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
	if request.ToolChoice.Set {
		required = append(required, FacetToolChoice)
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
	if request.Generation.HasOptions() || hasGenerationOptions(request.Raw, request.SourceFormat) {
		required = append(required, FacetGenerationOptions)
	}
	required = append(required, request.UnsupportedFacets...)
	return uniqueSorted(required)
}

// RequiredResponseEvents describes event families the selected client renderer
// must be able to encode if the provider emits them for this invocation.
func RequiredResponseEvents(request NormalizedRequest) []ResponseEventKind {
	events := []ResponseEventKind{EventTextDelta, EventResponseComplete}
	if len(request.Tools) > 0 {
		events = append(events, EventToolCallDelta)
	}
	if request.Thinking.Mode != "" && request.Thinking.Mode != "inherit" && request.Thinking.Mode != "disabled" || request.Thinking.Effort != "" || request.Thinking.BudgetTokens > 0 {
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

func hasGenerationOptions(raw map[string]any, format normalize.Format) bool {
	structural := map[string]bool{
		"model": true, "stream": true, "messages": true, "input": true, "contents": true,
		"tools": true, "reasoning_effort": true, "thinking": true, "reasoning": true,
		"conversation_id": true, "response_id": true, "previous_response_id": true,
		"operationPayload": true,
	}
	if format == normalize.FormatAnthropic {
		for _, key := range []string{"system", "tool_choice", "max_tokens", "temperature", "top_p", "stop_sequences", "metadata", "output_config"} {
			structural[key] = true
		}
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
	mapping.Losses = cloneLossRecords(mapping.Losses)
	return mapping
}

func validateLossRecord(loss LossRecord) error {
	const maxLossValueBytes = 4096
	const maxLossPathBytes = 256
	if loss.ID == "" || strings.TrimSpace(loss.ID) != loss.ID || strings.ContainsAny(loss.ID, " \t\r\n") {
		return fmt.Errorf("loss ID is invalid")
	}
	if loss.Requested == nil || loss.Effective == nil {
		return fmt.Errorf("requested and effective values are required")
	}
	if len(loss.SemanticPaths) == 0 {
		return fmt.Errorf("at least one semantic path is required")
	}
	requested, err := json.Marshal(loss.Requested)
	if err != nil {
		return fmt.Errorf("requested value is not JSON-compatible: %w", err)
	}
	if len(requested) > maxLossValueBytes {
		return fmt.Errorf("requested value exceeds %d encoded bytes", maxLossValueBytes)
	}
	effective, err := json.Marshal(loss.Effective)
	if err != nil {
		return fmt.Errorf("effective value is not JSON-compatible: %w", err)
	}
	if len(effective) > maxLossValueBytes {
		return fmt.Errorf("effective value exceeds %d encoded bytes", maxLossValueBytes)
	}
	for _, path := range loss.SemanticPaths {
		if path == "" || len(path) > maxLossPathBytes {
			return fmt.Errorf("semantic path is empty or exceeds %d bytes", maxLossPathBytes)
		}
	}
	return nil
}

func mergeLossRecord(records []LossRecord, incoming LossRecord) ([]LossRecord, error) {
	for index := range records {
		current := &records[index]
		if current.ID != incoming.ID {
			continue
		}
		requestedEqual, err := jsonValuesEqual(current.Requested, incoming.Requested)
		if err != nil || !requestedEqual {
			return records, fmt.Errorf("loss %q has conflicting requested values", incoming.ID)
		}
		effectiveEqual, err := jsonValuesEqual(current.Effective, incoming.Effective)
		if err != nil || !effectiveEqual {
			return records, fmt.Errorf("loss %q has conflicting effective values", incoming.ID)
		}
		current.SemanticPaths = appendUnique(current.SemanticPaths, incoming.SemanticPaths...)
		current.PolicySources = appendUnique(current.PolicySources, incoming.PolicySources...)
		return records, nil
	}
	cloned := cloneLossRecord(incoming)
	return append(records, cloned), nil
}

func jsonValuesEqual(left, right any) (bool, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func cloneLossRecords(records []LossRecord) []LossRecord {
	result := make([]LossRecord, len(records))
	for index, record := range records {
		result[index] = cloneLossRecord(record)
	}
	return result
}

func cloneLossRecord(record LossRecord) LossRecord {
	cloneValue := func(value any) any {
		encoded, err := json.Marshal(value)
		if err != nil {
			return value
		}
		var clone any
		if json.Unmarshal(encoded, &clone) != nil {
			return value
		}
		return clone
	}
	record.Requested = cloneValue(record.Requested)
	record.Effective = cloneValue(record.Effective)
	record.SemanticPaths = append([]string(nil), record.SemanticPaths...)
	record.PolicySources = append([]string(nil), record.PolicySources...)
	return record
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
