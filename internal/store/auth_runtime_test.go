package store

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestRenewalResetDoesNotClearFailureOfCurrentCredential(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	before, _ := s.ConnectionCredentialByID(connection.ID)
	commit, err := s.CompareAndSwapConnectionCredential(context.Background(), before, "new", CredentialRenewal)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthRuntime(AuthRuntime{ProviderNodeID: connection.ProviderNodeID, ConnectionID: connection.ID, Scope: kernel.ScopeConnection, Generation: "current-auth-generation", CredentialRevision: commit.Revision, Status: "invalid", Cause: string(kernel.CauseAuth), ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if accepted, err := s.ResetCredentialRoutingState(context.Background(), commit); err != nil || !accepted {
		t.Fatalf("activation reset=%v %v", accepted, err)
	}
	rows, err := s.AuthRuntimes()
	if err != nil || len(rows) != 1 || rows[0].CredentialRevision != commit.Revision {
		t.Fatalf("activation erased new token's actual failure: %+v %v", rows, err)
	}
}

func TestAuthRuntimeGenerationFenceAndRoundTrip(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	credential, _ := s.ConnectionCredentialByID(connection.ID)
	item := AuthRuntime{ProviderNodeID: connection.ProviderNodeID, ConnectionID: connection.ID, ModelRef: "model", RouteID: "route", Scope: kernel.ScopeOperation, OperationRef: extensions.Ref{Kind: "operation", ID: "generate", ContractVersion: 1}, Generation: "auth:operation:route:model", CredentialRevision: credential.Revision, Status: "blocked", Failures: 2, ObservedAt: time.Now()}
	if err := s.SaveAuthRuntime(item); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthRuntime(item); err != nil {
		t.Fatal(err)
	}
	items, err := s.AuthRuntimes()
	if err != nil || len(items) != 1 || items[0].Generation != item.Generation || items[0].Scope != item.Scope || items[0].OperationRef != item.OperationRef {
		t.Fatalf("auth runtimes=%#v err=%v", items, err)
	}
	if _, err := s.UpdateConnection(UpdateConnectionInput{ID: connection.ID, Secret: "rotated-secret"}); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.ConnectionCredentialByID(connection.ID)
	current := item
	current.Generation = "auth:operation:route:model:new"
	current.CredentialRevision = updated.Revision
	current.Status = "available"
	current.ObservedAt = item.ObservedAt.Add(time.Minute)
	if err := s.SaveAuthRuntime(current); err != nil {
		t.Fatal(err)
	}
	stale := item
	stale.ObservedAt = item.ObservedAt.Add(time.Hour)
	if err := s.SaveAuthRuntime(stale); err != nil {
		t.Fatal(err)
	}
	items, err = s.AuthRuntimes()
	if err != nil || len(items) != 1 || items[0].Generation != current.Generation || items[0].CredentialRevision != updated.Revision {
		t.Fatalf("stale auth generation persisted: %#v err=%v", items, err)
	}
}

func TestProviderAuthRuntimeDoesNotRequireConnection(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	item := AuthRuntime{ProviderNodeID: connection.ProviderNodeID, Scope: kernel.ScopeProvider, Generation: "provider-auth-generation", Status: "blocked", ObservedAt: time.Now()}
	if err := s.SaveAuthRuntime(item); err != nil {
		t.Fatal(err)
	}
	newer := item
	newer.Generation = "provider-auth-generation-new"
	newer.ObservedAt = item.ObservedAt.Add(time.Minute)
	newer.Status = "available"
	if err := s.SaveAuthRuntime(newer); err != nil {
		t.Fatal(err)
	}
	stale := item
	stale.Generation = "provider-auth-generation-old"
	stale.ObservedAt = item.ObservedAt.Add(time.Second)
	if err := s.SaveAuthRuntime(stale); err != nil {
		t.Fatal(err)
	}
	items, err := s.AuthRuntimes()
	if err != nil || len(items) != 1 || items[0].ConnectionID != "" || items[0].ModelRef != "" || items[0].RouteID != "" || items[0].OperationRef != (extensions.Ref{}) || items[0].Generation != newer.Generation {
		t.Fatalf("provider auth runtimes=%#v err=%v", items, err)
	}
}

func TestConnectionAuthRuntimeAllowsConnectionScopeIdentityOnly(t *testing.T) {
	s, connection := newCredentialRevisionFixture(t)
	defer s.Close()
	credential, _ := s.ConnectionCredentialByID(connection.ID)
	item := AuthRuntime{ProviderNodeID: connection.ProviderNodeID, ConnectionID: connection.ID, Scope: kernel.ScopeConnection, CredentialRevision: credential.Revision, Generation: "connection-auth-generation", Status: "blocked", ObservedAt: time.Now()}
	if err := s.SaveAuthRuntime(item); err != nil {
		t.Fatal(err)
	}
	items, err := s.AuthRuntimes()
	if err != nil || len(items) != 1 || items[0].Scope != kernel.ScopeConnection || items[0].ModelRef != "" || items[0].RouteID != "" || items[0].OperationRef != (extensions.Ref{}) {
		t.Fatalf("connection auth runtimes=%#v err=%v", items, err)
	}
}
