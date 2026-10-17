package daemon

import (
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
)

// Authentication records have their own durable lane and credential epoch;
// loading them must not overwrite principal-scoped cooldown or quota evidence.
func (d *Daemon) restoreAuthenticationHealth(routes map[string]kernel.Route) error {
	items, err := d.store.AuthRuntimes()
	if err != nil {
		return err
	}
	for _, item := range items {
		for _, route := range routes {
			if route.NodeID != item.ProviderNodeID {
				continue
			}
			switch item.Scope {
			case kernel.ScopeProvider:
			case kernel.ScopeConnection:
				if route.CredentialID != item.ConnectionID {
					continue
				}
			case kernel.ScopeRoute, kernel.ScopeRouteConnection:
				if route.CredentialID != item.ConnectionID || route.ID != item.RouteID || route.ExternalModel != item.ModelRef {
					continue
				}
			case kernel.ScopeOperation:
				binding, ok := route.OperationBindings[normalize.Operation(item.OperationRef.ID)]
				if !ok || binding.ContractVersion != item.OperationRef.ContractVersion || route.ID != item.RouteID || route.CredentialID != item.ConnectionID || route.ExternalModel != item.ModelRef {
					continue
				}
				route.SessionOperationRef = item.OperationRef
			default:
				continue
			}
			if item.Generation != route.HealthGeneration.ForAuthentication(item.Scope) {
				continue
			}
			d.policy.Health.Restore(route, runtimehealth.RouteHealth{Lane: runtimehealth.HealthAuthentication, OperationRef: item.OperationRef, Failures: item.Failures, Successes: item.Successes, Invalid: item.Status == "invalid", Generation: item.Generation, CooldownUntil: parseTime(item.BlockedUntil), LastError: item.LastError, LastCause: kernel.OutcomeCause(item.Cause), LastScope: item.Scope, LastEvidence: kernel.EvidenceSource(item.Evidence), Confidence: item.Confidence, LastAttempt: parseTime(item.LastAttempt), LastSuccess: parseTime(item.LastSuccess)})
		}
	}
	return nil
}
