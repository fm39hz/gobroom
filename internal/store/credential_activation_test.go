package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCredentialActivationRequiresEvidenceBeforeActive(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	expected, _ := s.ConnectionCredentialByID(connection.ID)
	commit, err := s.CompareAndSwapConnectionCredential(context.Background(), expected, "new-state")
	if err != nil {
		t.Fatal(err)
	}
	a, ok, err := s.CredentialActivation(context.Background(), commit.ConnectionID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	a.Status = ActivationActive
	if _, err := s.SaveCredentialActivation(context.Background(), a); err == nil {
		t.Fatal("activation claimed active without publication evidence")
	}
	a.Progress = CredentialActivationProgress{RoutingReset: true, ControlVersion: 2, ControlPublished: true, VersionPersisted: true, ServingVersion: 2, ServingPublished: true}
	if accepted, err := s.SaveCredentialActivation(context.Background(), a); err != nil || !accepted {
		t.Fatalf("valid activation evidence rejected: %v %v", accepted, err)
	}
}

func TestCredentialActivationCASIsAtomicAndSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/activation.db"
	s, connection := openCredentialRevisionFixture(t, path)
	expected, _ := s.ConnectionCredentialByID(connection.ID)
	if _, err := s.DB.Exec(`CREATE TRIGGER fail_credential_activation BEFORE INSERT ON credential_activations BEGIN SELECT RAISE(ABORT,'injected activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSwapConnectionCredential(context.Background(), expected, "secret-failed"); err == nil {
		t.Fatal("CAS succeeded despite activation write failure")
	}
	unchanged, ok := s.ConnectionCredentialByID(connection.ID)
	if !ok || unchanged.Secret != expected.Secret || unchanged.Revision != expected.Revision {
		t.Fatalf("credential changed despite transaction rollback: %#v", unchanged)
	}
	if _, ok, err := s.CredentialActivation(context.Background(), connection.ID); err != nil || ok {
		t.Fatalf("activation after rollback: found=%v err=%v", ok, err)
	}
	if _, err := s.DB.Exec(`DROP TRIGGER fail_credential_activation`); err != nil {
		t.Fatal(err)
	}
	commit, err := s.CompareAndSwapConnectionCredential(context.Background(), expected, "secret-b")
	if err != nil {
		t.Fatal(err)
	}
	activation, ok, err := s.CredentialActivation(context.Background(), connection.ID)
	if err != nil || !ok || activation.Status != ActivationPending || activation.Commit != commit {
		t.Fatalf("activation=%#v found=%v err=%v", activation, ok, err)
	}
	encoded, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["Commit"]; exists {
		t.Fatalf("activation exposed commit: %s", encoded)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, ok, err := reopened.CredentialActivation(context.Background(), connection.ID)
	if err != nil || !ok || restored.Commit != commit || restored.Status != ActivationPending {
		t.Fatalf("restored activation=%#v found=%v err=%v", restored, ok, err)
	}
}

func TestCredentialActivationSaveAndRoutingResetAreFenced(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	expected, _ := s.ConnectionCredentialByID(connection.ID)
	first, err := s.CompareAndSwapConnectionCredential(context.Background(), expected, "secret-b")
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := s.CredentialActivation(context.Background(), connection.ID)
	if err != nil || !ok {
		t.Fatalf("read first activation: found=%v err=%v", ok, err)
	}
	if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Secret: "secret-b-external"}); err != nil {
		t.Fatal(err)
	}
	job.Progress.ControlPublished = true
	if saved, err := s.SaveCredentialActivation(context.Background(), job); err != nil || saved {
		t.Fatalf("stale activation progress save: saved=%v err=%v", saved, err)
	}
	job.Status = ActivationSuperseded
	if saved, err := s.SaveCredentialActivation(context.Background(), job); err != nil || !saved {
		t.Fatalf("stale activation supersede: saved=%v err=%v", saved, err)
	}
	secondExpected, _ := s.ConnectionCredentialByID(connection.ID)
	second, err := s.CompareAndSwapConnectionCredential(context.Background(), secondExpected, "secret-c")
	if err != nil {
		t.Fatal(err)
	}
	job.Status = ActivationPending
	if saved, err := s.SaveCredentialActivation(context.Background(), job); err != nil || saved {
		t.Fatalf("stale activation overwrote newer row: saved=%v err=%v", saved, err)
	}
	current, ok, err := s.CredentialActivation(context.Background(), connection.ID)
	if err != nil || !ok || current.Commit != second || current.Progress.ControlPublished {
		t.Fatalf("new activation overwritten: %#v err=%v", current, err)
	}

	if err := s.SaveConnectionRuntime(ConnectionRuntime{ConnectionID: connection.ID, Status: "cooldown", ConsecutiveFailures: 4}); err != nil {
		t.Fatal(err)
	}
	stale := first
	if reset, err := s.ResetCredentialRoutingState(context.Background(), stale); err != nil || reset {
		t.Fatalf("stale routing reset: reset=%v err=%v", reset, err)
	}
	runtime, found, err := s.ConnectionRuntime(connection.ID)
	if err != nil || !found || runtime.Status != "cooldown" || runtime.ConsecutiveFailures != 4 {
		t.Fatalf("stale commit cleared runtime: %#v found=%v err=%v", runtime, found, err)
	}
	if reset, err := s.ResetCredentialRoutingState(context.Background(), second); err != nil || !reset {
		t.Fatalf("current routing reset: reset=%v err=%v", reset, err)
	}
	if err := s.SaveConnectionRuntime(ConnectionRuntime{ConnectionID: connection.ID, Status: "cooldown", ConsecutiveFailures: 2}); err != nil {
		t.Fatal(err)
	}
	node, err := s.ProviderNode(connection.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	setProviderDefinition(t, s, node, "store", "rotated-definition", node.DefinitionContractVersion)
	if reset, err := s.ResetCredentialRoutingState(context.Background(), second); err != nil || reset {
		t.Fatalf("old owner routing reset: reset=%v err=%v", reset, err)
	}
	runtime, found, err = s.ConnectionRuntime(connection.ID)
	if err != nil || !found || runtime.Status != "cooldown" || runtime.ConsecutiveFailures != 2 {
		t.Fatalf("old owner commit cleared runtime: %#v found=%v err=%v", runtime, found, err)
	}
}

func TestCredentialActivationOrdersNanosecondDeadlinesAndRoundTripsZero(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	firstExpected, _ := s.ConnectionCredentialByID(connection.ID)
	firstCommit, err := s.CompareAndSwapConnectionCredential(context.Background(), firstExpected, "secret-b")
	if err != nil {
		t.Fatal(err)
	}
	secondConnection, err := s.CreateConnection(CreateConnectionInput{ProviderNodeID: connection.ProviderNodeID, Name: "second", CredentialType: "api_key", Secret: "secret-second"})
	if err != nil {
		t.Fatal(err)
	}
	secondExpected, _ := s.ConnectionCredentialByID(secondConnection.ID)
	secondCommit, err := s.CompareAndSwapConnectionCredential(context.Background(), secondExpected, "secret-second-b")
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := s.CredentialActivation(context.Background(), firstCommit.ConnectionID)
	if err != nil || !ok {
		t.Fatalf("first activation: found=%v err=%v", ok, err)
	}
	second, ok, err := s.CredentialActivation(context.Background(), secondCommit.ConnectionID)
	if err != nil || !ok {
		t.Fatalf("second activation: found=%v err=%v", ok, err)
	}
	base := time.Date(2026, time.October, 17, 12, 0, 0, 0, time.UTC)
	first.NextAttempt = base.Add(900 * time.Nanosecond)
	second.NextAttempt = base.Add(100 * time.Nanosecond)
	if saved, err := s.SaveCredentialActivation(context.Background(), first); err != nil || !saved {
		t.Fatalf("save first deadline: saved=%v err=%v", saved, err)
	}
	if saved, err := s.SaveCredentialActivation(context.Background(), second); err != nil || !saved {
		t.Fatalf("save second deadline: saved=%v err=%v", saved, err)
	}
	items, err := s.CredentialActivations(context.Background(), 10)
	if err != nil || len(items) != 2 || items[0].Commit.ConnectionID != secondCommit.ConnectionID || !items[0].NextAttempt.Equal(second.NextAttempt) || items[1].Commit.ConnectionID != firstCommit.ConnectionID {
		t.Fatalf("nanosecond deadline order=%#v err=%v", items, err)
	}
	second.NextAttempt = time.Time{}
	if saved, err := s.SaveCredentialActivation(context.Background(), second); err != nil || !saved {
		t.Fatalf("save zero deadline: saved=%v err=%v", saved, err)
	}
	reloaded, ok, err := s.CredentialActivation(context.Background(), secondCommit.ConnectionID)
	if err != nil || !ok || !reloaded.NextAttempt.IsZero() {
		t.Fatalf("zero deadline=%#v found=%v err=%v", reloaded.NextAttempt, ok, err)
	}
}

func TestPendingCredentialActivationsAndGenerationRotate(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	node, err := s.ProviderNode(connection.ProviderNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCatalogModel(UpsertCatalogModelInput{ID: "activation-route", ProviderNodeID: node.ID, Kind: "custom", ExternalID: "model", DisplayName: "Model", ConnectionIDs: []string{connection.ID}}); err != nil {
		t.Fatal(err)
	}
	routes, err := s.Routes()
	if err != nil || len(routes) != 1 {
		t.Fatalf("routes=%#v err=%v", routes, err)
	}
	beforeGeneration := routes[0].ConnectionGeneration
	expected, _ := s.ConnectionCredentialByID(connection.ID)
	if _, err := s.CompareAndSwapConnectionCredential(context.Background(), expected, expected.Secret); err != nil {
		t.Fatal(err)
	}
	routes, err = s.Routes()
	if err != nil || len(routes) != 1 || routes[0].ConnectionGeneration == beforeGeneration {
		t.Fatalf("generation did not rotate: routes=%#v err=%v", routes, err)
	}
	items, err := s.PendingCredentialActivations(context.Background(), time.Now().Add(time.Minute), 0)
	if err != nil || len(items) != 1 || items[0].Commit.ConnectionID != connection.ID {
		t.Fatalf("pending activations=%#v err=%v", items, err)
	}
	next, found, err := s.NextCredentialActivation(context.Background())
	if err != nil || !found || next.IsZero() {
		t.Fatalf("next activation=%v found=%v err=%v", next, found, err)
	}
}
