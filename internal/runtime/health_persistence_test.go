package runtime

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHealthObservationQueueCoalescesAndFlushesOnClose(t *testing.T) {
	var mu sync.Mutex
	var saved []HealthObservation
	queue := NewHealthObservationQueue(2, func(observation HealthObservation) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, observation)
		return nil
	}, nil)
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model"}
	first := HealthObservation{Route: route, State: RouteHealth{Failures: 1, LastScope: kernel.ScopeRoute}}
	latest := HealthObservation{Route: route, State: RouteHealth{Failures: 2, LastScope: kernel.ScopeRoute}}
	if !queue.Enqueue(first) || !queue.Enqueue(latest) {
		t.Fatal("same-scope observations should coalesce within a full queue")
	}
	queue.Close()
	done := make(chan struct{})
	go func() { queue.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not flush pending health observations")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saved) != 1 || saved[0].State.Failures != 2 {
		t.Fatalf("saved observations=%#v", saved)
	}
}

func TestHealthObservationQueueKeepsNewestAttempt(t *testing.T) {
	for _, replaceWithNewer := range []bool{false, true} {
		t.Run(fmt.Sprintf("replaceWithNewer=%t", replaceWithNewer), func(t *testing.T) {
			var mu sync.Mutex
			var saved []HealthObservation
			queue := NewHealthObservationQueue(1, func(observation HealthObservation) error {
				mu.Lock()
				defer mu.Unlock()
				saved = append(saved, observation)
				return nil
			}, nil)
			route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model"}
			newestAttempt := time.Date(2026, time.October, 18, 10, 0, 0, 0, time.UTC)
			newerBlocking := HealthObservation{Route: route, State: RouteHealth{
				Failures: 1, LastScope: kernel.ScopeConnection, Lane: HealthOperational,
				LastAttempt: newestAttempt, LastError: "upstream unavailable",
			}}
			olderSuccess := HealthObservation{Route: route, State: RouteHealth{
				Successes: 1, LastScope: kernel.ScopeConnection, Lane: HealthOperational,
				LastSuccess: newestAttempt.Add(-time.Minute),
			}}
			if !queue.Enqueue(newerBlocking) || !queue.Enqueue(olderSuccess) {
				t.Fatal("same-lane observations should coalesce even when the queue is full")
			}

			latestAttempt := HealthObservation{Route: route, State: RouteHealth{
				Failures: 2, LastScope: kernel.ScopeConnection, Lane: HealthOperational,
				LastAttempt: newestAttempt.Add(time.Minute), LastError: "still unavailable",
			}}
			expected := newerBlocking
			if replaceWithNewer {
				if !queue.Enqueue(latestAttempt) {
					t.Fatal("a newer same-lane observation should replace the pending observation")
				}
				expected = latestAttempt
			}
			queue.Close()
			done := make(chan struct{})
			go func() { queue.Run(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("close did not flush pending health observations")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(saved) != 1 || !saved[0].State.LastAttempt.Equal(expected.State.LastAttempt) || saved[0].State.Failures != expected.State.Failures {
				t.Fatalf("saved observations=%#v", saved)
			}
		})
	}
}

func TestHealthObservationQueueBoundsDistinctScopes(t *testing.T) {
	queue := NewHealthObservationQueue(1, func(HealthObservation) error { return nil }, nil)
	defer queue.Close()
	first := HealthObservation{Route: kernel.Route{ID: "a", CredentialID: "a", ExternalModel: "a"}, State: RouteHealth{LastScope: kernel.ScopeRoute}}
	second := HealthObservation{Route: kernel.Route{ID: "b", CredentialID: "b", ExternalModel: "b"}, State: RouteHealth{LastScope: kernel.ScopeRoute}}
	if !queue.Enqueue(first) || queue.Enqueue(second) {
		t.Fatal("bounded queue must reject a new distinct scope when at capacity")
	}
}

func TestHealthObservationQueueKeepsLanesDistinct(t *testing.T) {
	var saved []HealthObservation
	queue := NewHealthObservationQueue(2, func(observation HealthObservation) error { saved = append(saved, observation); return nil }, nil)
	route := kernel.Route{ID: "route", CredentialID: "connection"}
	operational := HealthObservation{Route: route, State: RouteHealth{LastScope: kernel.ScopeConnection, Lane: HealthOperational}}
	authentication := HealthObservation{Route: route, State: RouteHealth{LastScope: kernel.ScopeConnection, Lane: HealthAuthentication}}
	if !queue.Enqueue(operational) || !queue.Enqueue(authentication) {
		t.Fatal("independent lanes did not fit in queue")
	}
	queue.Close()
	done := make(chan struct{})
	go func() { queue.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue did not flush")
	}
	if len(saved) != 2 {
		t.Fatalf("lane observations coalesced: %#v", saved)
	}
}

func TestHealthObservationIdentityUsesLaneGeneration(t *testing.T) {
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", HealthGeneration: kernel.RouteHealthGeneration{Provider: "p1", Connection: "principal", Credential: "credential-r1", Model: "m1"}}
	first := HealthObservation{Route: route, State: RouteHealth{LastScope: kernel.ScopeConnection, Lane: HealthOperational}}
	newCredential := route
	newCredential.HealthGeneration.Credential = "credential-r2"
	second := first
	second.Route = newCredential
	if healthObservationIdentity(first) != healthObservationIdentity(second) {
		t.Fatal("operational observations split across credential epochs")
	}
	first.State.Lane = HealthAuthentication
	second.State.Lane = HealthAuthentication
	if healthObservationIdentity(first) == healthObservationIdentity(second) {
		t.Fatal("authentication epochs shared a queue identity")
	}
}

func TestHealthObservationQueueReportsSinkFailure(t *testing.T) {
	wantErr := errors.New("store unavailable")
	failed := make(chan error, 1)
	queue := NewHealthObservationQueue(1, func(HealthObservation) error { return wantErr }, func(_ HealthObservation, err error) { failed <- err })
	if !queue.Enqueue(HealthObservation{Route: kernel.Route{ID: "route"}, State: RouteHealth{LastScope: kernel.ScopeRoute}}) {
		t.Fatal("enqueue failed")
	}
	queue.Close()
	done := make(chan struct{})
	go func() { queue.Run(); close(done) }()
	select {
	case err := <-failed:
		if !errors.Is(err, wantErr) {
			t.Fatalf("reported error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("storage failure was not reported")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer did not stop")
	}
}
