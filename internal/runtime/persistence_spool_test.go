package runtime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

func TestDurablePersistenceSpoolReplaysAndAcknowledgesHealthAndQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-spool.json")
	spool, err := OpenDurablePersistenceSpool(path, 1<<20, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", BaseURL: "https://secret-in-url.invalid", HealthGeneration: kernel.RouteHealthGeneration{Provider: "p1", Connection: "c1", Model: "m1"}}
	health := HealthObservation{Route: route, State: RouteHealth{Invalid: true, LastCause: kernel.CauseAuth, LastScope: kernel.ScopeConnection, Generation: route.HealthGeneration.For(kernel.ScopeConnection)}}
	dead, err := spool.DeferHealth(health, 1, errors.New("database busy"))
	if err != nil || dead {
		t.Fatalf("defer health dead=%t err=%v", dead, err)
	}
	remaining := float64(0)
	quotaSnapshot := quota.Snapshot{ProviderNodeID: "provider", ConnectionID: "connection", ModelRef: "model", WindowName: "daily", Generation: "c1m1", Authority: quota.AuthorityResponseHeader, ObservedAt: time.Now(), Remaining: &remaining, Source: "response_header"}
	if dead, err := spool.DeferQuota(quotaSnapshot, 1, errors.New("database busy")); err != nil || dead {
		t.Fatalf("defer quota dead=%t err=%v", dead, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("spool permissions=%v err=%v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte(route.BaseURL)) {
		t.Fatalf("spool contains route endpoint details: err=%v", err)
	}

	reopened, err := OpenDurablePersistenceSpool(path, 1<<20, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	pending := reopened.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending spool records=%#v", pending)
	}
	var gotHealth, gotQuota bool
	for _, record := range pending {
		if observation, ok := record.HealthObservation(); ok {
			gotHealth = observation.Route.ID == route.ID && observation.Route.BaseURL == "" && observation.State.Invalid && observation.State.Generation == health.State.Generation
		}
		if snapshot, ok := record.QuotaSnapshot(); ok {
			gotQuota = snapshot.Generation == quotaSnapshot.Generation && snapshot.Remaining != nil && *snapshot.Remaining == 0
		}
	}
	if !gotHealth || !gotQuota {
		t.Fatalf("spooled records did not round-trip: %#v", pending)
	}
	if err := reopened.Acknowledge(pending[0].Key); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Pending()) != 1 {
		t.Fatalf("acknowledged spool record remained pending: %#v", reopened.Pending())
	}
}

func TestDurablePersistenceSpoolDeadLettersAndBoundsRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-spool.json")
	spool, err := OpenDurablePersistenceSpool(path, 1<<20, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	route := kernel.Route{ID: "route", CredentialID: "connection", ExternalModel: "model"}
	observation := HealthObservation{Route: route, State: RouteHealth{LastScope: kernel.ScopeRoute, LastAttempt: time.Now()}}
	if dead, err := spool.DeferHealth(observation, 1, errors.New("disk busy")); err != nil || dead {
		t.Fatalf("first failure dead=%t err=%v", dead, err)
	}
	if dead, err := spool.DeferHealth(observation, 1, errors.New("disk busy")); err != nil || !dead {
		t.Fatalf("threshold failure dead=%t err=%v", dead, err)
	}
	if len(spool.Pending()) != 0 || spool.DeadLetterCount() != 1 {
		t.Fatalf("dead-letter counts: pending=%#v dead=%d", spool.Pending(), spool.DeadLetterCount())
	}
	second := HealthObservation{Route: kernel.Route{ID: "other-route", CredentialID: "other", ExternalModel: "other-model"}, State: RouteHealth{LastScope: kernel.ScopeRoute}}
	if HealthSpoolKey(observation) == HealthSpoolKey(second) {
		t.Fatalf("test identities unexpectedly match: %s", HealthSpoolKey(second))
	}
	if _, err := spool.DeferHealth(second, 1, errors.New("overflow")); !errors.Is(err, ErrPersistenceSpoolFull) {
		t.Fatalf("record capacity error=%v status=%#v", err, spool.Status())
	}
	reopened, err := OpenDurablePersistenceSpool(path, 1<<20, 1, 2)
	if err != nil || reopened.DeadLetterCount() != 1 {
		t.Fatalf("dead-letter was not durable: spool=%#v err=%v", reopened, err)
	}
	if count, err := reopened.RequeueDeadLetters(); err != nil || count != 1 || reopened.Status().Pending != 1 || reopened.Status().DeadLetters != 0 {
		t.Fatalf("dead-letter requeue count=%d status=%#v err=%v", count, reopened.Status(), err)
	}
}
