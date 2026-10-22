package kernel

import (
	"fmt"
	"math"
	"slices"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const (
	defaultUsageMaxValues        = 32
	defaultUsageMaxExtensionByte = 65536
	maxAccountingIssues          = 32
	accountingIssueOverflowCode  = "overflow"
	invalidCommonUsageCode       = "invalid_usage_counter"
)

// UsageLimits bounds extension usage accepted by a host.
type UsageLimits struct {
	MaxValues         int
	MaxExtensionBytes int64
}

// DefaultUsageLimits returns the host defaults for extension usage.
func DefaultUsageLimits() UsageLimits {
	return UsageLimits{MaxValues: defaultUsageMaxValues, MaxExtensionBytes: defaultUsageMaxExtensionByte}
}

// Clone returns an independently owned usage snapshot, including nested
// compatibility evidence and extension payloads.
func (event UsageEvent) Clone() UsageEvent {
	cloneInt64 := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		return new(*value)
	}
	cloneFloat64 := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		return new(*value)
	}

	event.InputTokens = cloneInt64(event.InputTokens)
	event.OutputTokens = cloneInt64(event.OutputTokens)
	event.CachedReadTokens = cloneInt64(event.CachedReadTokens)
	event.CachedWriteTokens = cloneInt64(event.CachedWriteTokens)
	event.ReasoningTokens = cloneInt64(event.ReasoningTokens)
	event.EstimatedCost = cloneFloat64(event.EstimatedCost)
	event.OutputTokensPerSecond = cloneFloat64(event.OutputTokensPerSecond)
	event.Extensions = cloneUsageValues(event.Extensions)
	event.AccountingIssues = slices.Clone(event.AccountingIssues)
	event.CompatibilityLosses = cloneLossRecords(event.CompatibilityLosses)
	if event.CompatibilityPlan != nil {
		plan := *event.CompatibilityPlan
		plan.Mappings = slices.Clone(plan.Mappings)
		for index := range plan.Mappings {
			plan.Mappings[index].Paths = slices.Clone(plan.Mappings[index].Paths)
		}
		plan.RequestTransformSteps = cloneRequestTransformSteps(plan.RequestTransformSteps)
		plan.ResponseTransformSteps = cloneResponseTransformSteps(plan.ResponseTransformSteps)
		plan.ArtifactTransfers = slices.Clone(plan.ArtifactTransfers)
		plan.ArtifactOutputs = slices.Clone(plan.ArtifactOutputs)
		plan.TransformFailures = slices.Clone(plan.TransformFailures)
		event.CompatibilityPlan = &plan
	}
	return event
}

// HasReportedUsage reports whether any common counter, measurement, or typed
// extension value is present. A known zero is still reported usage.
func (event UsageEvent) HasReportedUsage() bool {
	return event.InputTokens != nil || event.OutputTokens != nil ||
		event.CachedReadTokens != nil || event.CachedWriteTokens != nil ||
		event.ReasoningTokens != nil || event.EstimatedCost != nil ||
		event.OutputTokensPerSecond != nil || len(event.Extensions) != 0
}

// BoundedIssues returns an owned, coalesced diagnostic list using the same
// policy as streaming snapshot merging.
func (event UsageEvent) BoundedIssues() []UsageIssue {
	return coalesceUsageIssues(event.AccountingIssues)
}

// SanitizeCommon returns an owned snapshot with invalid optional metrics
// replaced by unknown values and a constant accounting issue. Valid siblings
// and explicit zero values are retained; the caller's snapshot is untouched.
func (event UsageEvent) SanitizeCommon() UsageEvent {
	event = event.Clone()
	invalid := false
	if !validUsageUpdateKind(event.UpdateKind) {
		event.UpdateKind = UsagePartialSnapshot
		invalid = true
	}
	for _, counter := range []**int64{
		&event.InputTokens, &event.OutputTokens, &event.CachedReadTokens,
		&event.CachedWriteTokens, &event.ReasoningTokens,
	} {
		if invalidCommonCounter(*counter) {
			*counter = nil
			invalid = true
		}
	}
	for _, measure := range []**float64{&event.EstimatedCost, &event.OutputTokensPerSecond} {
		if invalidCommonMeasure(*measure) {
			*measure = nil
			invalid = true
		}
	}
	if invalid {
		event.AccountingIssues = append(event.AccountingIssues, UsageIssue{Code: invalidCommonUsageCode})
	}
	event.AccountingIssues = coalesceUsageIssues(event.AccountingIssues)
	return event
}

func invalidCommonCounter(value *int64) bool {
	return value != nil && *value < 0
}

func invalidCommonMeasure(value *float64) bool {
	return value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0)
}

// ValidateCommon validates values with common semantics. Missing values are
// unknown and valid; present counters and costs must be nonnegative, and
// floating point measurements must be finite.
func (event UsageEvent) ValidateCommon() error {
	if !validUsageUpdateKind(event.UpdateKind) {
		return fmt.Errorf("unsupported usage update kind %q", event.UpdateKind)
	}
	for _, counter := range []struct {
		name  string
		value *int64
	}{
		{"input tokens", event.InputTokens},
		{"output tokens", event.OutputTokens},
		{"cached read tokens", event.CachedReadTokens},
		{"cached write tokens", event.CachedWriteTokens},
		{"reasoning tokens", event.ReasoningTokens},
	} {
		if invalidCommonCounter(counter.value) {
			return fmt.Errorf("%s must be nonnegative", counter.name)
		}
	}
	for _, measure := range []struct {
		name  string
		value *float64
	}{
		{"estimated cost", event.EstimatedCost},
		{"output tokens per second", event.OutputTokensPerSecond},
	} {
		if invalidCommonMeasure(measure.value) {
			return fmt.Errorf("%s must be finite and nonnegative", measure.name)
		}
	}
	return nil
}

func validUsageUpdateKind(kind UsageUpdateKind) bool {
	return kind == "" || kind == UsagePartialSnapshot || kind == UsageFullSnapshot
}

// MergeProviderUsage applies an already-normalized usage observation. Partial
// observations replace present values and retain absent values. Full snapshots
// replace common quantities and extensions, including unknown nil values. The
// result is always an independently owned full snapshot.
func MergeProviderUsage(current, update UsageEvent) UsageEvent {
	merged := current.Clone()
	fullSnapshot := update.UpdateKind == UsageFullSnapshot
	if fullSnapshot {
		merged.InputTokens = nil
		merged.OutputTokens = nil
		merged.CachedReadTokens = nil
		merged.CachedWriteTokens = nil
		merged.ReasoningTokens = nil
		merged.EstimatedCost = nil
		merged.OutputTokensPerSecond = nil
		merged.Extensions = nil
	}
	if !update.At.IsZero() {
		merged.At = update.At
	}
	if update.LogicalModel != "" {
		merged.LogicalModel = update.LogicalModel
	}
	if update.ProviderNodeID != "" {
		merged.ProviderNodeID = update.ProviderNodeID
	}
	if update.ExternalModel != "" {
		merged.ExternalModel = update.ExternalModel
	}
	if update.ConnectionID != "" {
		merged.ConnectionID = update.ConnectionID
	}
	if update.Status != "" {
		merged.Status = update.Status
	}
	if update.Latency != 0 {
		merged.Latency = update.Latency
	}
	if !update.FirstByteAt.IsZero() {
		merged.FirstByteAt = update.FirstByteAt
	}
	if update.TTFT != 0 {
		merged.TTFT = update.TTFT
	}
	if update.RequestClass != "" {
		merged.RequestClass = update.RequestClass
	}
	if update.SessionID != "" {
		merged.SessionID = update.SessionID
	}
	if update.CompatibilityFidelity != "" {
		merged.CompatibilityFidelity = update.CompatibilityFidelity
	}
	if len(update.CompatibilityLosses) != 0 {
		merged.CompatibilityLosses = cloneLossRecords(update.CompatibilityLosses)
	}
	if update.CompatibilityPlan != nil {
		clone := update.Clone()
		merged.CompatibilityPlan = clone.CompatibilityPlan
	}
	if update.InputTokens != nil {
		merged.InputTokens = new(*update.InputTokens)
	}
	if update.OutputTokens != nil {
		merged.OutputTokens = new(*update.OutputTokens)
	}
	if update.CachedReadTokens != nil {
		merged.CachedReadTokens = new(*update.CachedReadTokens)
	}
	if update.CachedWriteTokens != nil {
		merged.CachedWriteTokens = new(*update.CachedWriteTokens)
	}
	if update.ReasoningTokens != nil {
		merged.ReasoningTokens = new(*update.ReasoningTokens)
	}
	if update.EstimatedCost != nil {
		merged.EstimatedCost = new(*update.EstimatedCost)
	}
	if update.OutputTokensPerSecond != nil {
		merged.OutputTokensPerSecond = new(*update.OutputTokensPerSecond)
	}
	if fullSnapshot {
		merged.Extensions = cloneUsageValues(update.Extensions)
	} else {
		merged.Extensions = mergeUsageValues(merged.Extensions, update.Extensions)
	}
	merged.AccountingIssues = coalesceUsageIssues(merged.AccountingIssues, update.AccountingIssues)
	merged.UpdateKind = UsageFullSnapshot
	return merged
}

func cloneUsageValues(values []extensions.UsageValue) []extensions.UsageValue {
	result := slices.Clone(values)
	for index := range result {
		result[index].Value = slices.Clone(result[index].Value)
	}
	return result
}

func mergeUsageValues(current, update []extensions.UsageValue) []extensions.UsageValue {
	result := current
	for _, value := range update {
		owned := value
		owned.Value = slices.Clone(value.Value)
		found := false
		for index := range result {
			if result[index].TypeRef == value.TypeRef {
				result[index] = owned
				found = true
				break
			}
		}
		if !found {
			result = append(result, owned)
		}
	}
	return result
}

func coalesceUsageIssues(groups ...[]UsageIssue) []UsageIssue {
	allEmpty := true
	for _, group := range groups {
		if len(group) != 0 {
			allEmpty = false
			break
		}
	}
	if allEmpty {
		return nil
	}
	result := make([]UsageIssue, 0, maxAccountingIssues+1)
	seen := make(map[UsageIssue]struct{}, maxAccountingIssues+1)
	overflow := false
	for _, group := range groups {
		for _, issue := range group {
			if issue.Code == accountingIssueOverflowCode && issue.TypeRef == (extensions.Ref{}) {
				overflow = true
				continue
			}
			if _, ok := seen[issue]; ok {
				continue
			}
			if len(result) == maxAccountingIssues {
				overflow = true
				continue
			}
			seen[issue] = struct{}{}
			result = append(result, issue)
		}
	}
	if overflow {
		result = append(result, UsageIssue{Code: accountingIssueOverflowCode})
	}
	return result
}
