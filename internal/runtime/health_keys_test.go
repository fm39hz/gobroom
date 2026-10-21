package runtime

import (
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHealthKeysCombinationsAndOrder(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		route := kernel.Route{ID: "route"}
		if mask&1 != 0 {
			route.SessionOperationRef = extensions.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 2}
		}
		if mask&2 != 0 {
			route.CredentialID = "credential"
		}
		if mask&4 != 0 {
			route.NodeID = "provider"
		}

		want := []breakerKey{
			scopeKey(route),
			outcomeLaneKey(route, kernel.ScopeRoute, HealthAuthentication),
		}
		if mask&1 != 0 {
			want = append(want,
				outcomeScopeKey(route, kernel.ScopeOperation),
				outcomeLaneKey(route, kernel.ScopeOperation, HealthAuthentication),
			)
		}
		if mask&2 != 0 {
			want = append(want,
				outcomeScopeKey(route, kernel.ScopeRouteConnection),
				outcomeScopeKey(route, kernel.ScopeConnection),
				outcomeLaneKey(route, kernel.ScopeRouteConnection, HealthAuthentication),
				outcomeLaneKey(route, kernel.ScopeConnection, HealthAuthentication),
			)
		}
		if mask&4 != 0 {
			want = append(want,
				outcomeScopeKey(route, kernel.ScopeProvider),
				outcomeLaneKey(route, kernel.ScopeProvider, HealthAuthentication),
			)
		}

		var got []breakerKey
		for key := range healthKeys(route) {
			got = append(got, key)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("mask %03b: health keys = %#v, want %#v", mask, got, want)
		}
	}
}

var healthKeysBenchmarkCount int

func TestHealthKeysStopsAtConsumerBoundary(t *testing.T) {
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "account", SessionOperationRef: extensions.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 1}}
	for _, stopAt := range []int{1, 3, 7, 10} {
		seen := 0
		healthKeys(route)(func(breakerKey) bool { seen++; return seen < stopAt })
		if seen != stopAt {
			t.Fatalf("yield continued beyond stop: seen=%d stop=%d", seen, stopAt)
		}
	}
}

func BenchmarkHealthKeys(b *testing.B) {
	route := kernel.Route{
		ID: "route", NodeID: "provider", CredentialID: "credential",
		SessionOperationRef: extensions.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 2},
	}
	b.ReportAllocs()
	b.ResetTimer()
	count := 0
	for i := 0; i < b.N; i++ {
		for range healthKeys(route) {
			count++
		}
	}
	healthKeysBenchmarkCount = count
}
