package runtime

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/quota"
)

type operationSpoolQuotaSink struct {
	saved []quota.Snapshot
}

func (s *operationSpoolQuotaSink) SaveQuotaSnapshot(snapshot quota.Snapshot) error {
	s.saved = append(s.saved, snapshot)
	return nil
}

func TestOperationPersistenceQueuesKeepExactOperationIdentities(t *testing.T) {
	route := operationTestRoute()
	generation := route.HealthGeneration.For(kernel.ScopeOperation)
	refs := []struct {
		ref extensions.Ref
	}{
		{operationTestRef(string(normalize.OperationChatGenerate), 1)},
		{operationTestRef(string(normalize.OperationChatGenerate), 2)},
		{operationTestRef("chat.cancel", 1)},
	}

	var savedHealth []HealthObservation
	healthQueue := NewHealthObservationQueue(4, func(observation HealthObservation) error {
		savedHealth = append(savedHealth, observation)
		return nil
	}, nil)
	for _, item := range refs {
		bound := operationTestBound(route, item.ref)
		if !healthQueue.Enqueue(HealthObservation{Route: bound, State: RouteHealth{LastScope: kernel.ScopeOperation, OperationRef: item.ref, Generation: generation}}) {
			t.Fatalf("health enqueue rejected operation %v", item.ref)
		}
	}
	healthQueue.Close()
	healthQueue.Run()
	if len(savedHealth) != len(refs) {
		t.Fatalf("health queue coalesced distinct operation refs: got %d observations", len(savedHealth))
	}
	seenHealth := map[extensions.Ref]bool{}
	for _, observation := range savedHealth {
		seenHealth[observation.State.OperationRef] = true
	}
	for _, item := range refs {
		if !seenHealth[item.ref] {
			t.Errorf("health queue lost operation %v", item.ref)
		}
	}

	quotaSink := &operationSpoolQuotaSink{}
	quotaQueue := NewQuotaSnapshotQueue(quotaSink, 4, nil)
	for _, item := range refs {
		snapshot := quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "daily", OperationRef: item.ref}
		if !quotaQueue.Enqueue(snapshot) {
			t.Fatalf("quota enqueue rejected operation %v", item.ref)
		}
	}
	quotaQueue.Close()
	quotaQueue.Run()
	if len(quotaSink.saved) != len(refs) {
		t.Fatalf("quota queue coalesced distinct typed identities: got %d snapshots", len(quotaSink.saved))
	}
	seenQuota := map[extensions.Ref]bool{}
	for _, snapshot := range quotaSink.saved {
		seenQuota[snapshot.Identity().Operation] = true
	}
	for _, item := range refs {
		if !seenQuota[item.ref] {
			t.Errorf("quota queue lost operation %v", item.ref)
		}
	}
}

func TestOperationPersistenceSpoolRoundTripsExactRefsAndGlobalQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation-spool.json")
	spool, err := OpenDurablePersistenceSpool(path, 1<<20, 16, 5)
	if err != nil {
		t.Fatal(err)
	}
	route := operationTestRoute()
	operationGeneration := route.HealthGeneration.For(kernel.ScopeOperation)
	operationRefs := []extensions.Ref{
		operationTestRef(string(normalize.OperationChatGenerate), 1),
		operationTestRef(string(normalize.OperationChatGenerate), 2),
	}
	healthObservations := make([]HealthObservation, 0, len(operationRefs))
	for i, ref := range operationRefs {
		bound := operationTestBound(route, ref)
		observation := HealthObservation{Route: bound, State: RouteHealth{LastScope: kernel.ScopeOperation, OperationRef: ref, Generation: operationGeneration, LastAttempt: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)}}
		if dead, err := spool.DeferHealth(observation, 1, nil); err != nil || dead {
			t.Fatalf("defer operation health %v: dead=%t err=%v", ref, dead, err)
		}
		healthObservations = append(healthObservations, observation)
	}

	quotaSnapshots := make([]quota.Snapshot, 0, 3)
	for _, ref := range append(operationRefs, extensions.Ref{}) {
		snapshot := quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "daily", OperationRef: ref, Generation: "quota-generation", ObservedAt: time.Date(2026, 1, 1, 1, len(quotaSnapshots), 0, 0, time.UTC)}
		if dead, err := spool.DeferQuota(snapshot, 1, nil); err != nil || dead {
			t.Fatalf("defer quota operation %v: dead=%t err=%v", ref, dead, err)
		}
		quotaSnapshots = append(quotaSnapshots, snapshot)
	}

	reopened, err := OpenDurablePersistenceSpool(path, 1<<20, 16, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range healthObservations {
		record, ok := reopened.Record(HealthSpoolKey(expected))
		if !ok {
			t.Fatalf("missing health spool record for %v", expected.State.OperationRef)
		}
		got, ok := record.HealthObservation()
		if !ok || got.Route.SessionOperationRef != expected.State.OperationRef || got.State.OperationRef != expected.State.OperationRef || got.State.Generation != operationGeneration {
			t.Errorf("health operation did not round-trip exactly: %#v, ok=%t", got, ok)
		}
	}
	for _, expected := range quotaSnapshots {
		record, ok := reopened.Record(QuotaSpoolKey(expected))
		if !ok {
			t.Fatalf("missing quota spool record for operation %v", expected.OperationRef)
		}
		got, ok := record.QuotaSnapshot()
		if !ok || got.OperationRef != expected.OperationRef || got.Generation != expected.Generation {
			t.Errorf("quota operation did not round-trip exactly: %#v, ok=%t", got, ok)
		}
	}
	if quotaSnapshots[0].OperationRef == quotaSnapshots[1].OperationRef || QuotaSpoolKey(quotaSnapshots[0]) == QuotaSpoolKey(quotaSnapshots[1]) || QuotaSpoolKey(quotaSnapshots[0]) == QuotaSpoolKey(quotaSnapshots[2]) {
		t.Fatal("operation versions or global quota collided in durable spool identity")
	}
}
