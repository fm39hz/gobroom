package runtime

import (
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	"github.com/fm39hz/gobroom/internal/store"
)

type quotaSnapshotMemorySink struct {
	mu      sync.Mutex
	saved   map[string]quota.Snapshot
	updates chan quota.Snapshot
}

type gatedQuotaSink struct {
	mu      sync.Mutex
	saved   map[string]quota.Snapshot
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedQuotaSink) SaveQuotaSnapshot(snapshot quota.Snapshot) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	s.mu.Lock()
	s.saved[quotaSnapshotQueueKey(snapshot)] = snapshot
	s.mu.Unlock()
	return nil
}

func TestQuotaSnapshotQueueResetFencesInflightPersistence(t *testing.T) {
	sink := &gatedQuotaSink{saved: map[string]quota.Snapshot{}, entered: make(chan struct{}), release: make(chan struct{})}
	queue := NewQuotaSnapshotQueue(sink, 4, nil)
	snapshot := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", WindowName: "daily"}
	if !queue.Enqueue(snapshot) {
		t.Fatal("enqueue failed")
	}
	runDone := make(chan struct{})
	go func() { queue.Run(); close(runDone) }()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not begin the queued write")
	}
	resetDone := make(chan error, 1)
	go func() {
		resetDone <- queue.ResetConnection(snapshot.ConnectionID, func() error {
			sink.mu.Lock()
			delete(sink.saved, quotaSnapshotQueueKey(snapshot))
			sink.mu.Unlock()
			return nil
		})
	}()
	select {
	case err := <-resetDone:
		t.Fatalf("reset completed before the in-flight write was fenced: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(sink.release)
	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("connection reset did not complete after the write finished")
	}
	queue.Close()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("quota writer did not stop")
	}
	sink.mu.Lock()
	_, found := sink.saved[quotaSnapshotQueueKey(snapshot)]
	sink.mu.Unlock()
	if found {
		t.Fatal("in-flight stale quota snapshot survived configuration reset")
	}
	if queue.ResetProvider("provider", nil) == nil {
		t.Fatal("nil provider reset callback must be rejected")
	}
}

func (s *quotaSnapshotMemorySink) SaveQuotaSnapshot(snapshot quota.Snapshot) error {
	key := quotaSnapshotQueueKey(snapshot)
	s.mu.Lock()
	s.saved[key] = snapshot
	s.mu.Unlock()
	s.updates <- snapshot
	return nil
}

func (s *quotaSnapshotMemorySink) snapshot(key string) (quota.Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.saved[key]
	return value, ok
}

func TestQuotaSnapshotQueueCoalescesAndFlushesOnClose(t *testing.T) {
	sink := &quotaSnapshotMemorySink{saved: map[string]quota.Snapshot{}, updates: make(chan quota.Snapshot, 4)}
	queue := NewQuotaSnapshotQueue(sink, 2, nil)
	first := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", WindowName: "weekly", Source: "error_body"}
	first.Remaining = floatPointer(0)
	latest := first
	latest.Remaining = floatPointer(3)
	if !queue.Enqueue(first) || !queue.Enqueue(latest) {
		t.Fatal("expected same-key quota observations to coalesce in bounded queue")
	}
	if queue.Enqueue(quota.Snapshot{ProviderNodeID: "provider", ModelRef: "other", WindowName: "daily"}) != true {
		t.Fatal("expected another identity to fit in the queue")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		queue.Run()
	}()
	queue.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quota queue did not flush and stop")
	}
	saved, ok := sink.snapshot(quotaSnapshotQueueKey(first))
	if !ok || saved.Remaining == nil || *saved.Remaining != 3 {
		t.Fatalf("latest coalesced quota snapshot=%#v, found=%t", saved, ok)
	}
	select {
	case <-sink.updates:
	case <-time.After(time.Second):
		t.Fatal("queued quota snapshots were not persisted")
	}
	if queue.Enqueue(first) {
		t.Fatal("closed quota queue accepted a late write")
	}
}

func TestErrorOutcomeQuotaPersistsAndRestoresRouteGateAcrossRestart(t *testing.T) {
	dbPath := t.TempDir() + "/quota.db"
	stateStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(3 * time.Minute).UTC().Truncate(time.Second)
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", Enabled: true}
	gate := NewPolicyGate()
	queue := NewQuotaSnapshotQueue(stateStore, 8, nil)
	gate.SetOutcomeQuotaObserver(func(snapshot quota.Snapshot) { queue.Enqueue(snapshot) })
	queueDone := make(chan struct{})
	go func() {
		defer close(queueDone)
		queue.Run()
	}()
	body := []byte(`{"error":{"code":"insufficient_quota","message":"quota exceeded","reset_at":"` + reset.Format(time.RFC3339) + `"}}`)
	outcome := (provider.HTTPJSONErrorClassifier{}).ClassifyOutcome(429, nil, body)
	gate.ObserveOutcome(route, outcome)
	if gate.Usable(route, time.Now()) {
		t.Fatal("route should be blocked immediately from real-attempt evidence")
	}
	queue.Close()
	select {
	case <-queueDone:
	case <-time.After(time.Second):
		t.Fatal("quota snapshot writer did not flush before shutdown")
	}
	snapshots, err := stateStore.QuotaSnapshots()
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("persisted quota snapshots=%#v err=%v", snapshots, err)
	}
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := NewPolicyGate()
	reloaded, err := reopened.QuotaSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range reloaded {
		restored.SetQuota(snapshot)
	}
	if restored.Usable(route, time.Now()) {
		t.Fatal("reloaded quota state should keep the route blocked before reset")
	}
	if !restored.Usable(route, reset.Add(time.Second)) {
		t.Fatal("persisted route should become eligible after reset without a healthcheck")
	}
}

func quotaSnapshotQueueKey(snapshot quota.Snapshot) string {
	return snapshot.ProviderNodeID + "\x00" + snapshot.ConnectionID + "\x00" + snapshot.ModelRef + "\x00" + snapshot.WindowName
}

func floatPointer(value float64) *float64 { return &value }
