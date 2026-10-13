package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
	runtimehealth "github.com/fm39hz/gobroom/internal/runtime"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestRuntimePersistenceSpoolsFailuresAndReplaysAfterRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := root + "/gobroom.db"
	state, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('provider','Provider','http://provider','openai_chat','p'); INSERT INTO connections(id,provider_node_id,name,credential_type) VALUES('connection','provider','Account','api_key')`); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	spool, err := runtimehealth.OpenDurablePersistenceSpool(root+"/runtime-spool.json", 1<<20, 16, 8)
	if err != nil {
		t.Fatal(err)
	}
	closedStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedStore.Close(); err != nil {
		t.Fatal(err)
	}
	route := kernel.Route{ID: "route@connection", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", HealthGeneration: kernel.RouteHealthGeneration{Provider: "provider-v1", Connection: "connection-v1", Model: "model-v1"}}
	health := runtimehealth.RouteHealth{Invalid: true, LastCause: kernel.CauseAuth, LastScope: kernel.ScopeConnection, Generation: route.HealthGeneration.For(kernel.ScopeConnection), LastAttempt: time.Now()}
	d := &Daemon{store: closedStore, persistenceSpool: spool}
	if err := d.persistHealthObservation(runtimehealth.HealthObservation{Route: route, State: health}); err != nil {
		t.Fatalf("failed DB write was not durably spooled: %v", err)
	}
	remaining := float64(0)
	quotaState := quota.Snapshot{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "daily", Generation: route.HealthGeneration.ForQuota(route.CredentialID, route.ExternalModel), Authority: quota.AuthorityResponseHeader, ObservedAt: time.Now(), Remaining: &remaining, Source: "response_header"}
	if err := d.persistQuotaSnapshot(quotaState); err != nil {
		t.Fatalf("failed quota write was not durably spooled: %v", err)
	}
	if len(spool.Pending()) != 2 {
		t.Fatalf("spooled records=%#v", spool.Pending())
	}

	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.store = reopened
	defer reopened.Close()
	d.policy = runtimehealth.NewPolicyGate()
	d.policy.SetGenerations(map[string]kernel.Route{route.ID: route})
	healthQueue := runtimehealth.NewHealthObservationQueue(16, d.persistHealthObservation, nil)
	quotaQueue := runtimehealth.NewQuotaSnapshotQueue(quotaSnapshotSinkFunc(d.persistQuotaSnapshot), 16, nil)
	healthDone, quotaDone := make(chan struct{}), make(chan struct{})
	go func() { healthQueue.Run(); close(healthDone) }()
	go func() { quotaQueue.Run(); close(quotaDone) }()
	defer func() {
		healthQueue.Close()
		quotaQueue.Close()
		<-healthDone
		<-quotaDone
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(spool.Due(time.Now())) != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	d.replayPersistenceSpool(healthQueue, quotaQueue)
	for time.Now().Before(deadline) {
		healthRows, healthErr := reopened.ConnectionRuntimes()
		quotaRows, quotaErr := reopened.QuotaSnapshots()
		if healthErr == nil && quotaErr == nil && len(healthRows) == 1 && healthRows[0].Status == "invalid" && healthRows[0].Generation == route.HealthGeneration.For(kernel.ScopeConnection) && len(quotaRows) == 1 && quotaRows[0].Generation == quotaState.Generation {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	healthRows, healthErr := reopened.ConnectionRuntimes()
	quotaRows, quotaErr := reopened.QuotaSnapshots()
	if healthErr != nil || quotaErr != nil || len(healthRows) != 1 || healthRows[0].Status != "invalid" || healthRows[0].Generation != route.HealthGeneration.For(kernel.ScopeConnection) || len(quotaRows) != 1 || quotaRows[0].Generation != quotaState.Generation {
		t.Fatalf("spool replay health=%#v err=%v quota=%#v err=%v", healthRows, healthErr, quotaRows, quotaErr)
	}
	if len(spool.Pending()) != 0 {
		t.Fatalf("successfully replayed records were not acknowledged: %#v", spool.Pending())
	}
}

func TestPersistenceIPCReportsAndRequeuesDeadLetters(t *testing.T) {
	spool, err := runtimehealth.OpenDurablePersistenceSpool(t.TempDir()+"/runtime-spool.json", 1<<20, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	observation := runtimehealth.HealthObservation{Route: kernel.Route{ID: "route"}, State: runtimehealth.RouteHealth{LastScope: kernel.ScopeRoute, LastAttempt: time.Now()}}
	if dead, err := spool.DeferHealth(observation, 1, errors.New("storage offline")); err != nil || !dead {
		t.Fatalf("deadLetter=%t err=%v", dead, err)
	}
	daemon := &Daemon{persistenceSpool: spool}
	status := daemon.handleIPC(context.Background(), IPCRequest{ID: "status", Method: "persistence.status"})
	value, ok := status.Result.(runtimehealth.PersistenceSpoolStatus)
	if !status.OK || !ok || value.DeadLetters != 1 || value.Pending != 0 {
		t.Fatalf("persistence status=%#v", status)
	}
	retry := daemon.handleIPC(context.Background(), IPCRequest{ID: "retry", Method: "persistence.retry"})
	if !retry.OK || spool.Status().DeadLetters != 0 || spool.Status().Pending != 1 {
		t.Fatalf("persistence retry=%#v status=%#v", retry, spool.Status())
	}
}
