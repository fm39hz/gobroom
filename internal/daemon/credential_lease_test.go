package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func credentialLeaseRoute(t *testing.T, d *Daemon, connectionID string) kernel.Route {
	t.Helper()
	route := credentialRefreshRoute(t, d, connectionID)
	route.ID = "oauth:chat.generate"
	route.SessionOperationRef = operations.ChatGenerateRef()
	return route
}

func TestCredentialLeaseDispatchRejectsChangedLiveBinding(t *testing.T) {
	for _, mutation := range []string{"same-secret resave", "owner rebind", "disable", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			flow := &ownerGateCredentialFlow{}
			d, connectionID := cancellationAuthDaemon(t, flow)
			route := credentialLeaseRoute(t, d, connectionID)
			lease, err := d.acquireCredentialLease(context.Background(), route)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			if _, err := lease.Borrow(context.Background(), kernel.CredentialLeaseScopeForRoute(route)); err != nil {
				t.Fatalf("lease did not allow local borrowing before mutation: %v", err)
			}

			switch mutation {
			case "same-secret resave":
				if _, err := d.store.UpdateConnection(store.UpdateConnectionInput{ID: connectionID, Secret: "original-secret"}); err != nil {
					t.Fatal(err)
				}
			case "owner rebind":
				rebindCredentialDefinition(t, d, connectionID, "different-definition")
			case "disable":
				disabled := false
				if _, err := d.store.UpdateConnection(store.UpdateConnectionInput{ID: connectionID, Enabled: &disabled}); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := d.store.DeleteConnection(connectionID); err != nil {
					t.Fatal(err)
				}
			}

			if err := lease.ValidateForDispatch(context.Background(), kernel.CredentialLeaseScopeForRoute(route)); err == nil {
				t.Fatal("dispatch accepted a lease after its stored binding changed")
			}
			if got := flow.resolveCalls.Load(); got != 1 {
				t.Fatalf("dispatch fence performed %d Resolve calls after acquisition; want only the acquisition call", got)
			}
			if got := flow.refreshCalls.Load(); got != 0 {
				t.Fatalf("dispatch fence performed %d Refresh calls; want none", got)
			}
		})
	}
}

func TestIssueCredentialLeaseRejectsStalePrincipal(t *testing.T) {
	flow := &ownerGateCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialLeaseRoute(t, d, connectionID)
	material, err := d.resolveCredential(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := d.store.ConnectionCredentialByID(connectionID)
	if !ok {
		t.Fatal("fixture credential is unavailable")
	}
	if _, err := d.store.CompareAndSwapConnectionCredential(context.Background(), stored, stored.Secret, store.PrincipalReplacement); err != nil {
		t.Fatal(err)
	}
	if _, err := d.issueCredentialLease(context.Background(), route, material); err == nil {
		t.Fatal("lease issuance accepted material resolved for a replaced principal")
	}
}

func TestRefreshCredentialLeaseIssuesNewRevisionAndFencesOldLease(t *testing.T) {
	flow := &ownerGateCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialLeaseRoute(t, d, connectionID)
	oldLease, err := d.acquireCredentialLease(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	defer oldLease.Release()

	newLease, err := d.refreshCredentialLease(context.Background(), route, oldLease)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Release()
	if newLease.Generation() == oldLease.Generation() {
		t.Fatalf("refreshed lease generation stayed at %q", oldLease.Generation())
	}
	credential, err := newLease.Borrow(context.Background(), kernel.CredentialLeaseScopeForRoute(route))
	if err != nil {
		t.Fatalf("new lease could not be borrowed: %v", err)
	}
	if credential.Secret != "refreshed-secret" {
		t.Fatalf("new lease credential secret = %q, want refreshed material", credential.Secret)
	}
	if err := newLease.ValidateForDispatch(context.Background(), kernel.CredentialLeaseScopeForRoute(route)); err != nil {
		t.Fatalf("new lease failed its live dispatch fence: %v", err)
	}
	if err := oldLease.ValidateForDispatch(context.Background(), kernel.CredentialLeaseScopeForRoute(route)); err == nil {
		t.Fatal("old lease remained dispatchable after refresh committed a new revision")
	}
}

type expiringCredentialFlow struct {
	ownerGateCredentialFlow
}

func (f *expiringCredentialFlow) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	f.resolveCalls.Add(1)
	expires := time.Now().Add(-time.Minute)
	if input.Secret == "refreshed-secret" {
		expires = time.Now().Add(time.Hour)
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret, ExpiresAt: expires}, nil
}

func TestAcquireCredentialLeaseRefreshesExpiredCredentialFirst(t *testing.T) {
	flow := &expiringCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialLeaseRoute(t, d, connectionID)
	lease, err := d.acquireCredentialLease(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	credential, err := lease.Borrow(context.Background(), kernel.CredentialLeaseScopeForRoute(route))
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "refreshed-secret" || !credential.ExpiresAt.After(time.Now()) {
		t.Fatalf("acquired lease contains unrefreshed or expired credential: secret=%q expires=%v", credential.Secret, credential.ExpiresAt)
	}
	if got := flow.refreshCalls.Load(); got != 1 {
		t.Fatalf("expired credential Refresh calls = %d, want 1", got)
	}
}

func TestCredentialLeaseHonorsCallerCancellation(t *testing.T) {
	flow := &ownerGateCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialLeaseRoute(t, d, connectionID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.acquireCredentialLease(ctx, route); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire with canceled caller context returned %v, want context.Canceled", err)
	}
	if flow.resolveCalls.Load() != 0 {
		t.Fatal("canceled acquisition reached the auth flow")
	}

	lease, err := d.acquireCredentialLease(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := lease.ValidateForDispatch(ctx, kernel.CredentialLeaseScopeForRoute(route)); !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatch with canceled caller context returned %v, want context.Canceled", err)
	}
}
