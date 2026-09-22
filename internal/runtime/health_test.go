package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHealthGateCooldownAndRecovery(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route-a", Enabled: true}
	if !gate.Usable(route, time.Now()) {
		t.Fatal("new route should be usable")
	}
	gate.MarkFailure(route, kernel.ErrorCooldown, errors.New("429"))
	if gate.Usable(route, time.Now()) {
		t.Fatal("failed route should be cooled down")
	}
	gate.MarkSuccess(route)
	if !gate.Usable(route, time.Now()) {
		t.Fatal("successful route should recover")
	}
}

func TestHealthGateRecordsPassiveCauseAndRanksSuccessfulRoute(t *testing.T) {
	gate := NewPolicyGate()
	first := kernel.Route{ID: "first", Enabled: true}
	second := kernel.Route{ID: "second", Enabled: true}
	gate.ObserveOutcome(first, kernel.ClassifiedOutcome{Cause: kernel.CauseRateLimited, Retry: kernel.RetryAfter, Confidence: .9, Message: "rate limited"})
	gate.ObserveOutcome(second, kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess, Retry: kernel.RetryNow, Confidence: 1})
	ordered := gate.RankRoutes([]kernel.Route{first, second}, time.Now())
	if len(ordered) != 2 || ordered[0].ID != "second" {
		t.Fatalf("successful route should rank first: %#v", ordered)
	}
	state := gate.Health.Snapshot()["second"]
	if state.LastCause != kernel.CauseSuccess || state.Successes != 1 {
		t.Fatalf("unexpected success state: %#v", state)
	}
}

func TestHealthGatePreservesAuthoritativeRetryAt(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route", Enabled: true}
	reset := time.Now().Add(2 * time.Hour)
	gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseQuotaExhausted, Retry: kernel.RetryAfter, RetryAt: reset, Confidence: 1})
	state := gate.Snapshot()[route.ID]
	if state.CooldownUntil.Before(reset.Add(-time.Second)) {
		t.Fatalf("authoritative reset was capped or lost: %v < %v", state.CooldownUntil, reset)
	}
}

func TestHealthGateAppliesConnectionScopeAcrossRoutes(t *testing.T) {
	gate := NewHealthGate()
	first := kernel.Route{ID: "model-a", NodeID: "provider", CredentialID: "conn"}
	second := kernel.Route{ID: "model-b", NodeID: "provider", CredentialID: "conn"}
	other := kernel.Route{ID: "model-c", NodeID: "provider", CredentialID: "other"}
	gate.ObserveOutcome(first, kernel.ClassifiedOutcome{Cause: kernel.CauseQuotaExhausted, Scope: kernel.ScopeConnection, Retry: kernel.RetryAfter, RetryAt: time.Now().Add(time.Hour)})
	if gate.Usable(first, time.Now()) || gate.Usable(second, time.Now()) {
		t.Fatal("connection-scoped block did not cover all routes on the connection")
	}
	if !gate.Usable(other, time.Now()) {
		t.Fatal("connection-scoped block leaked to another connection")
	}
}
