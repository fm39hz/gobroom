package kernel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

var ErrRequiredUsage = errors.New("required attempt accounting is incomplete")

type usageInvocationKey struct{}

// usageAttempt belongs to one actual Execute call, not one candidate or stream
// frame. Neither a provider callback nor a usage source owns its identity.
type usageAttempt struct {
	mu                              sync.Mutex
	kernel                          *Kernel
	invocationID, id                string
	route                           Route
	logicalModel, workload, session string
	operationRef                    extensions.Ref
	started                         time.Time
	evidence                        UsageEvidence
	usage                           UsageEvent
	enriched, completed, finalized  bool
	completionErr                   error
	compatibility                   *CompatibilityPlanSummary
	fidelity                        CompatibilityFidelity
	losses                          []LossRecord
	limits                          UsageLimits
}

func (k *Kernel) newUsageAttempt(invocationID string, route Route, request NormalizedRequest) *usageAttempt {
	operationRef := extensions.Ref{Kind: "operation", ID: string(request.Operation), ContractVersion: request.OperationContractVersion}
	limits := k.resolvedUsageLimits()
	if definition, ok := k.Operations.Resolve(operationRef); ok {
		for _, bound := range []int64{definition.ResourceBounds.MaxBufferedBytes, definition.ResourceBounds.MaxOutputBytes} {
			if bound > 0 {
				limits.MaxExtensionBytes = min(limits.MaxExtensionBytes, bound)
			}
		}
	}
	return &usageAttempt{kernel: k, invocationID: invocationID, id: rand.Text(), route: route,
		logicalModel: request.Model, workload: requestClass(request), session: request.Session.ID,
		operationRef: operationRef, limits: limits, started: time.Now()}
}

func (a *usageAttempt) admit(event UsageEvent) UsageEvent {
	return a.kernel.admitUsageWithin(event, a.limits)
}

func (a *usageAttempt) observe(event UsageEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finalized || a.completed {
		return
	}
	a.usage = a.admit(MergeProviderUsage(a.usage, a.admit(event)))
}

func (a *usageAttempt) setEvidence(evidence UsageEvidence) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evidence = evidence
}

func (a *usageAttempt) enrichLocked(ctx context.Context) {
	if a.enriched {
		return
	}
	a.enriched = true
	if source := a.kernel.UsageSources[a.route.UsageSourceRef]; source != nil {
		evidence := a.evidence
		evidence.Headers = evidence.Headers.Clone()
		evidence.ErrorBody = append(json.RawMessage(nil), evidence.ErrorBody...)
		enriched, err := source.EnrichUsage(ctx, a.route, evidence, a.usage.Clone())
		if err != nil {
			a.usage.AccountingIssues = coalesceUsageIssues(a.usage.AccountingIssues, []UsageIssue{{Code: "usage_enrichment_failed"}})
		} else {
			a.usage = a.admit(MergeProviderUsage(a.usage, a.admit(enriched)))
		}
	}
}

func (a *usageAttempt) complete(ctx context.Context, event UsageEvent) (UsageEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.completed {
		return a.snapshotLocked("ok"), a.completionErr
	}
	a.usage = a.admit(MergeProviderUsage(a.usage, a.admit(event)))
	a.enrichLocked(ctx)
	a.completed = true
	result := a.snapshotLocked("ok")
	if definition, ok := a.kernel.Operations.Resolve(a.operationRef); ok {
		requirements := definition.UsageRequirements
		missing := requirements.RequireReported && !result.HasReportedUsage()
		for _, required := range requirements.RequiredExtensions {
			found := false
			for _, value := range result.Extensions {
				if value.TypeRef == required {
					found = true
					break
				}
			}
			missing = missing || !found
		}
		if missing {
			a.usage.AccountingIssues = coalesceUsageIssues(a.usage.AccountingIssues, []UsageIssue{{Code: "required_usage_missing"}})
			a.completionErr = WrapResponseFailure(ResponseFailureUnknown, ErrRequiredUsage)
			return a.snapshotLocked("error"), a.completionErr
		}
	}
	return result, nil
}

func (a *usageAttempt) snapshotLocked(status string) UsageEvent {
	event := a.usage.Clone()
	event.UpdateKind = UsageFullSnapshot
	event.InvocationID, event.AttemptID = a.invocationID, a.id
	event.At, event.Status = time.Now(), status
	event.LogicalModel, event.ProviderNodeID, event.ConnectionID, event.ExternalModel = a.logicalModel, a.route.NodeID, a.route.CredentialID, a.route.ExternalModel
	event.Latency = time.Since(a.started)
	event.RequestClass, event.SessionID = a.workload, a.session
	event.CompatibilityFidelity, event.CompatibilityLosses = a.fidelity, cloneLossRecords(a.losses)
	if a.compatibility != nil {
		event.CompatibilityPlan = (UsageEvent{CompatibilityPlan: a.compatibility}).Clone().CompatibilityPlan
	}
	if event.OutputTokens != nil && event.Latency > 0 {
		event.OutputTokensPerSecond = new(float64(*event.OutputTokens) / event.Latency.Seconds())
	} else {
		event.OutputTokensPerSecond = nil
	}
	return event
}

// finish also records a dispatch with no counters. Absence is evidence about
// accounting completeness, never a fabricated zero or a retry permission.
func (a *usageAttempt) finish(ctx context.Context, status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finishLocked(ctx, status)
}

func (a *usageAttempt) finishSuccess(ctx context.Context, event UsageEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finalized {
		return
	}
	a.usage = a.admit(event)
	a.compatibility = event.CompatibilityPlan
	a.fidelity, a.losses = event.CompatibilityFidelity, event.CompatibilityLosses
	a.finishLocked(ctx, "ok")
}

func (a *usageAttempt) setCompatibility(plan CompatibilityPlan) {
	a.mu.Lock()
	defer a.mu.Unlock()
	summary := SummarizeCompatibilityPlan(plan)
	a.compatibility = &summary
	a.fidelity, a.losses = plan.Fidelity, cloneLossRecords(plan.Losses)
}

func (a *usageAttempt) finishLocked(ctx context.Context, status string) {
	if a.finalized {
		return
	}
	a.enrichLocked(ctx)
	if ctx.Err() != nil && !errors.Is(context.Cause(ctx), ErrOperationDeadline) {
		status = "cancelled"
	}
	a.finalized = true
	event := a.snapshotLocked(status)
	a.usage, a.evidence = UsageEvent{}, UsageEvidence{}
	a.compatibility, a.losses = nil, nil
	a.kernel.EmitUsage(event)
}

// admitUsage checks extension count/encoded bytes/schema before retaining or
// cloning values. All entry points, including direct decoder hooks, use it.
func (k *Kernel) resolvedUsageLimits() UsageLimits {
	limits := k.UsageLimits
	if limits.MaxValues <= 0 || limits.MaxExtensionBytes <= 0 {
		return DefaultUsageLimits()
	}
	return limits
}

func (k *Kernel) admitUsage(event UsageEvent) UsageEvent {
	return k.admitUsageWithin(event, k.resolvedUsageLimits())
}

func (k *Kernel) admitUsageWithin(event UsageEvent, limits UsageLimits) UsageEvent {
	values, issues := event.Extensions, event.AccountingIssues
	event.Extensions, event.AccountingIssues = nil, nil
	event = event.SanitizeCommon()
	for index, issue := range issues {
		if index == maxAccountingIssues {
			event.AccountingIssues = append(event.AccountingIssues, UsageIssue{Code: accountingIssueOverflowCode})
			break
		}
		if len(issue.Code) > 128 || len(issue.TypeRef.ID) > 256 || len(issue.TypeRef.Kind) > 64 {
			event.AccountingIssues = append(event.AccountingIssues, UsageIssue{Code: "invalid_usage_issue"})
		} else {
			event.AccountingIssues = append(event.AccountingIssues, issue)
		}
	}
	var retained int64 = 2 // JSON array brackets, with envelope/ref overhead below.
	seen := make(map[extensions.Ref]struct{}, min(len(values), limits.MaxValues))
	for index, value := range values {
		if index >= limits.MaxValues {
			event.AccountingIssues = append(event.AccountingIssues, UsageIssue{Code: "usage_values_limit"})
			break
		}
		issue := ""
		switch {
		case int64(len(value.TypeRef.ID)) > limits.MaxExtensionBytes-retained,
			int64(len(value.TypeRef.Kind)) > limits.MaxExtensionBytes-retained,
			int64(len(value.Value)) > limits.MaxExtensionBytes-retained:
			issue = "usage_bytes_limit"
		case k.UsageCatalog == nil:
			issue = "unregistered_usage_value"
		default:
			if _, duplicate := seen[value.TypeRef]; duplicate {
				issue = "duplicate_usage_value"
			} else if err := k.UsageCatalog.ValidateUsageValueWithin(value, limits.MaxExtensionBytes-retained); err != nil {
				issue = "invalid_usage_value"
			}
		}
		if issue == "" {
			encoded, err := json.Marshal(value)
			size := int64(len(encoded))
			if len(event.Extensions) > 0 {
				size++
			}
			if err != nil {
				issue = "invalid_usage_value"
			} else if size > limits.MaxExtensionBytes-retained {
				issue = "usage_bytes_limit"
			} else {
				retained += size
				seen[value.TypeRef] = struct{}{}
				value.Value = append(json.RawMessage(nil), value.Value...)
				event.Extensions = append(event.Extensions, value)
			}
		}
		if issue != "" {
			event.AccountingIssues = append(event.AccountingIssues, UsageIssue{Code: issue})
		}
	}
	event.AccountingIssues = event.BoundedIssues()
	return event
}
