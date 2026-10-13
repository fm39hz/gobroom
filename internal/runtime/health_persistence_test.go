package runtime

import (
	"errors"
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

func TestHealthObservationQueueBoundsDistinctScopes(t *testing.T) {
	queue := NewHealthObservationQueue(1, func(HealthObservation) error { return nil }, nil)
	defer queue.Close()
	first := HealthObservation{Route: kernel.Route{ID: "a", CredentialID: "a", ExternalModel: "a"}, State: RouteHealth{LastScope: kernel.ScopeRoute}}
	second := HealthObservation{Route: kernel.Route{ID: "b", CredentialID: "b", ExternalModel: "b"}, State: RouteHealth{LastScope: kernel.ScopeRoute}}
	if !queue.Enqueue(first) || queue.Enqueue(second) {
		t.Fatal("bounded queue must reject a new distinct scope when at capacity")
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
