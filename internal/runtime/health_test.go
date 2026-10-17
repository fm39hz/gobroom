package runtime

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHealthGateCooldownAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		if gate.Usable(route, time.Now()) {
			t.Fatal("in-flight success erased an active rate-limit cooldown")
		}
		time.Sleep(time.Minute)
		if !gate.Acquire(route, time.Now()) {
			t.Fatal("expired cooldown did not allow a real trial")
		}
		gate.MarkSuccess(route)
		if !gate.Usable(route, time.Now()) {
			t.Fatal("successful route should recover")
		}
	})
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

func TestHealthGateRestoreRetainsPersistedScope(t *testing.T) {
	gate := NewHealthGate()
	first := kernel.Route{ID: "model-a", NodeID: "provider", CredentialID: "conn"}
	second := kernel.Route{ID: "model-b", NodeID: "provider", CredentialID: "conn"}
	blockedUntil := time.Now().Add(time.Hour)
	gate.Restore(first, RouteHealth{Failures: 1, CooldownUntil: blockedUntil, LastCause: kernel.CauseCapacity, LastScope: kernel.ScopeProvider})
	if gate.Usable(first, time.Now()) || gate.Usable(second, time.Now()) {
		t.Fatal("restored provider-scoped failure must gate every route from that provider")
	}

	modelGate := NewHealthGate()
	modelGate.Restore(first, RouteHealth{Failures: 1, CooldownUntil: blockedUntil, LastCause: kernel.CauseCapacity, LastScope: kernel.ScopeRoute})
	if modelGate.Usable(first, time.Now()) || !modelGate.Usable(second, time.Now()) {
		t.Fatal("restored route-scoped failure must not gate another model")
	}
}

func TestHealthGateKeepsPermanentProviderRejectionsBlockedUntilRecovery(t *testing.T) {
	gate := NewHealthGate()
	first := kernel.Route{ID: "model-a", NodeID: "provider", CredentialID: "conn"}
	second := kernel.Route{ID: "model-b", NodeID: "provider", CredentialID: "conn"}
	other := kernel.Route{ID: "model-c", NodeID: "provider", CredentialID: "other"}
	gate.ObserveOutcome(first, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever, Confidence: 1})
	if gate.Usable(first, time.Now()) || gate.Usable(second, time.Now()) || !gate.Usable(other, time.Now()) {
		t.Fatal("connection auth rejection must invalidate all routes on that connection only")
	}
	if state, ok := gate.ScopedAuthState(first, kernel.ScopeConnection); !ok || !state.Invalid {
		t.Fatalf("auth rejection was not recorded in authentication lane: %#v %t", state, ok)
	}
	if _, ok := gate.ScopedState(first, kernel.ScopeConnection); ok {
		t.Fatal("operational scoped accessor exposed authentication state")
	}
	if !gate.ResetScope(first, kernel.ScopeConnection) || !gate.Usable(first, time.Now()) || !gate.Usable(second, time.Now()) {
		t.Fatal("explicit connection configuration update did not recover invalid routes")
	}

	gate.ObserveOutcome(first, kernel.ClassifiedOutcome{Cause: kernel.CauseModelNotFound, Effect: kernel.EffectRejected, Scope: kernel.ScopeRoute, Retry: kernel.RetryNever, Confidence: 1})
	if gate.Usable(first, time.Now()) || !gate.Usable(second, time.Now()) {
		t.Fatal("model-not-found must invalidate only the exact route")
	}
	if !gate.ClearInvalid(first, kernel.ScopeRoute) || !gate.Usable(first, time.Now()) {
		t.Fatal("positive discovery evidence did not recover invalid model route")
	}

	gate.ObserveOutcome(first, kernel.ClassifiedOutcome{Cause: kernel.CauseRequestInvalid, Effect: kernel.EffectRejected, Scope: kernel.ScopeRequest, Retry: kernel.RetryNever, Confidence: 1})
	if !gate.Usable(first, time.Now()) {
		t.Fatal("client request validation error must not invalidate a provider route")
	}
}

func TestHealthGateRestoresPermanentInvalidation(t *testing.T) {
	gate := NewHealthGate()
	gate.Restore(kernel.Route{CredentialID: "connection"}, RouteHealth{Lane: HealthAuthentication, Invalid: true, LastCause: kernel.CauseAuth, LastScope: kernel.ScopeConnection})
	route := kernel.Route{ID: "model", CredentialID: "connection"}
	if gate.Usable(route, time.Now()) || gate.Acquire(route, time.Now()) {
		t.Fatal("restored invalid connection must remain excluded without a deadline")
	}
	if len(gate.Snapshot()) != 0 {
		t.Fatal("scope-only restore created a synthetic visible route")
	}
	if !gate.ResetScope(route, kernel.ScopeConnection) || !gate.Usable(route, time.Now()) {
		t.Fatal("credential configuration change did not clear restored invalidation")
	}
}

func TestHealthGateFencesOutcomesAcrossConfigurationGenerations(t *testing.T) {
	gate := NewHealthGate()
	oldRoute := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "credential-v1", Model: "model-v1"}}
	gate.SetGenerations(map[string]kernel.Route{oldRoute.ID: oldRoute})
	gate.ObserveOutcome(oldRoute, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever, Confidence: 1})
	if gate.Usable(oldRoute, time.Now()) {
		t.Fatal("matching-generation auth rejection should block the connection")
	}

	newRoute := oldRoute
	newRoute.HealthGeneration.Connection = "credential-v2"
	gate.SetGenerations(map[string]kernel.Route{newRoute.ID: newRoute})
	if !gate.Usable(newRoute, time.Now()) {
		t.Fatal("configuration generation change did not clear the old connection invalidation")
	}
	gate.ObserveOutcome(oldRoute, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever, Confidence: 1})
	gate.Release(oldRoute)
	if !gate.Usable(newRoute, time.Now()) {
		t.Fatal("late outcome from an old request re-invalidated the new credential generation")
	}

	gate.Restore(newRoute, RouteHealth{Lane: HealthAuthentication, Invalid: true, Generation: oldRoute.HealthGeneration.ForAuthentication(kernel.ScopeConnection), LastCause: kernel.CauseAuth, LastScope: kernel.ScopeConnection})
	if !gate.Usable(newRoute, time.Now()) {
		t.Fatal("persisted health from an obsolete generation was restored")
	}
}

func TestHealthLanesKeepAuthenticationAndOperationalBlocksIndependent(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model"}
	until := time.Now().Add(time.Hour)
	var outcomes sync.WaitGroup
	outcomes.Add(2)
	go func() {
		defer outcomes.Done()
		gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever})
	}()
	go func() {
		defer outcomes.Done()
		gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseRateLimited, Scope: kernel.ScopeConnection, Retry: kernel.RetryAfter, RetryAt: until, Message: "429 from provider", Confidence: .8, Evidence: []kernel.EvidenceSource{kernel.EvidenceResponseHeader}})
	}()
	outcomes.Wait()
	if _, ok := gate.ScopedState(route, kernel.ScopeConnection); !ok {
		t.Fatal("operational state missing")
	}
	if _, ok := gate.ScopedAuthState(route, kernel.ScopeConnection); !ok {
		t.Fatal("authentication state missing")
	}
	gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess, Scope: kernel.ScopeConnection, Retry: kernel.RetryNow})
	op, _ := gate.ScopedState(route, kernel.ScopeConnection)
	auth, _ := gate.ScopedAuthState(route, kernel.ScopeConnection)
	if op.CooldownUntil.Before(until.Add(-time.Second)) || op.LastCause != kernel.CauseRateLimited || op.LastError != "429 from provider" || op.Confidence != .8 || op.LastEvidence != kernel.EvidenceResponseHeader || op.Successes != 1 || !auth.Invalid || gate.Usable(route, time.Now()) {
		t.Fatalf("success cleared active cooldown evidence or independent auth state: op=%#v auth=%#v", op, auth)
	}
	view := gate.Snapshot()[route.ID]
	if len(view.Authentication) == 0 || view.RouteHealth.CooldownUntil.Before(until.Add(-time.Second)) {
		t.Fatalf("snapshot hid active operational block or auth state: %#v", view)
	}
}

func TestDiscoveryRecoveryDoesNotClearAuthenticationFailure(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection"}
	gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeRoute, Retry: kernel.RetryNever})
	if gate.ClearInvalid(route, kernel.ScopeRoute) || gate.Usable(route, time.Now()) {
		t.Fatal("model discovery recovery cleared unrelated authentication invalidity")
	}
}

func TestHealthAuthenticationRenewalResetsOnlyAuthenticationLane(t *testing.T) {
	gate := NewPolicyGate()
	old := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "p1", Connection: "principal", Credential: "token-1", Model: "m1"}}
	gate.SetGenerations(map[string]kernel.Route{old.ID: old})
	gate.Health.ObserveOutcome(old, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever})
	gate.Health.ObserveOutcome(old, kernel.ClassifiedOutcome{Cause: kernel.CauseRateLimited, Scope: kernel.ScopeConnection, Retry: kernel.RetryAfter, RetryAt: time.Now().Add(time.Hour)})
	newRoute := old
	newRoute.HealthGeneration.Credential = "token-2"
	gate.SetGenerations(map[string]kernel.Route{newRoute.ID: newRoute})
	if auth, exists := gate.Health.ScopedAuthState(newRoute, kernel.ScopeConnection); exists && (auth.Invalid || auth.Failures != 0 || !auth.CooldownUntil.IsZero()) {
		t.Fatalf("credential renewal retained stale auth state: %#v", auth)
	}
	if operational, exists := gate.Health.ScopedState(newRoute, kernel.ScopeConnection); !exists || operational.CooldownUntil.Before(time.Now().Add(30*time.Minute)) {
		t.Fatalf("credential renewal cleared principal-scoped operational cooldown: %#v %t", operational, exists)
	}
	gate.Health.ObserveOutcome(newRoute, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever})
	principalChanged := newRoute
	principalChanged.HealthGeneration.Connection = "principal-2"
	gate.SetGenerations(map[string]kernel.Route{principalChanged.ID: principalChanged})
	if auth, exists := gate.Health.ScopedAuthState(principalChanged, kernel.ScopeConnection); exists && (auth.Invalid || auth.Failures != 0 || !auth.CooldownUntil.IsZero()) {
		t.Fatalf("principal change retained authentication state: %#v", auth)
	}
	if operational, exists := gate.Health.ScopedState(principalChanged, kernel.ScopeConnection); exists && (operational.Failures != 0 || !operational.CooldownUntil.IsZero()) {
		t.Fatalf("principal change retained operational state: %#v", operational)
	}
}

func TestHealthGenerationReplacementDropsBothLanes(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection"}
	gate.SetGenerations(map[string]kernel.Route{route.ID: route})
	gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseAuth, Effect: kernel.EffectRejected, Scope: kernel.ScopeConnection, Retry: kernel.RetryNever})
	gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseRateLimited, Scope: kernel.ScopeConnection, Retry: kernel.RetryAfter, RetryAt: time.Now().Add(time.Hour)})
	gate.SetGenerations(map[string]kernel.Route{})
	if len(gate.Snapshot()) != 0 || len(gate.scoped) != 0 || len(gate.trials) != 0 {
		t.Fatalf("route replacement retained health lane state: snapshot=%#v scoped=%#v trials=%#v", gate.Snapshot(), gate.scoped, gate.trials)
	}
}

func TestReplayHealthObservationFencesAuthenticationLaneGeneration(t *testing.T) {
	gate := NewPolicyGate()
	oldRoute := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "p1", Connection: "principal", Credential: "rev-1", Model: "m1"}}
	gate.SetGenerations(map[string]kernel.Route{oldRoute.ID: oldRoute})
	oldObservation := HealthObservation{Route: oldRoute, State: RouteHealth{Lane: HealthAuthentication, Invalid: true, LastCause: kernel.CauseAuth, LastScope: kernel.ScopeConnection, Generation: oldRoute.HealthGeneration.ForAuthentication(kernel.ScopeConnection), LastAttempt: time.Now()}}
	if accepted, _ := gate.ReplayHealthObservation(oldObservation, nil); !accepted {
		t.Fatal("matching authentication generation was rejected")
	}
	newRoute := oldRoute
	newRoute.HealthGeneration.Credential = "rev-2"
	gate.SetGenerations(map[string]kernel.Route{newRoute.ID: newRoute})
	if accepted, _ := gate.ReplayHealthObservation(oldObservation, nil); accepted {
		t.Fatal("stale authentication replay crossed credential generation")
	}
	if auth, exists := gate.Health.ScopedAuthState(newRoute, kernel.ScopeConnection); !exists || auth.Invalid || auth.Failures != 0 {
		t.Fatalf("stale authentication replay restored blocked state: %#v exists=%t", auth, exists)
	}
}

func TestHealthHalfOpenTracksAuthenticationAndOperationalTrialsSeparately(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route", CredentialID: "connection"}
	past := time.Now().Add(-time.Second)
	gate.Restore(route, RouteHealth{Lane: HealthOperational, Failures: 1, CooldownUntil: past, LastScope: kernel.ScopeRoute})
	gate.Restore(route, RouteHealth{Lane: HealthAuthentication, Failures: 1, CooldownUntil: past, LastScope: kernel.ScopeRoute})
	if !gate.Acquire(route, time.Now()) || gate.Acquire(route, time.Now()) {
		t.Fatal("half open trials were not single flight across both lanes")
	}
	gate.ObserveOutcome(route, kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess, Scope: kernel.ScopeRoute, Retry: kernel.RetryNow})
	state, _ := gate.ScopedAuthState(route, kernel.ScopeRoute)
	if state.Failures != 1 {
		t.Fatalf("operational success cleared authentication trial state: %#v", state)
	}
	gate.Release(route)
}

func TestHealthGateHalfOpenAllowsOneRealTrial(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route"}
	gate.Restore(route, RouteHealth{Failures: 1, CooldownUntil: time.Now().Add(-time.Second)})
	now := time.Now()
	if !gate.Acquire(route, now) {
		t.Fatal("first post-cooldown request should be admitted")
	}
	if gate.Acquire(route, now) {
		t.Fatal("second concurrent post-cooldown request should wait")
	}
	gate.Release(route)
	if !gate.Acquire(route, now) {
		t.Fatal("trial should be released after the real attempt")
	}
}

func TestPolicyGateSessionAffinityPrefersLastSuccessfulRoute(t *testing.T) {
	gate := NewPolicyGate()
	first := kernel.Route{ID: "first", Enabled: true}
	second := kernel.Route{ID: "second", Enabled: true}
	gate.ObserveUsage(second, kernel.UsageEvent{At: time.Now(), Status: "ok", SessionID: "session-1"})
	ordered := gate.RankRoutesForSession([]kernel.Route{first, second}, time.Now(), "text", "session-1")
	if ordered[0].ID != "second" {
		t.Fatalf("session route was not preferred: %#v", ordered)
	}
}
