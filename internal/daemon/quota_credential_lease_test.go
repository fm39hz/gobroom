package daemon

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

type quotaLeaseTestTransport struct{ calls int }

func (t *quotaLeaseTestTransport) ID() string { return "quota-test-transport" }
func (t *quotaLeaseTestTransport) Execute(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	t.calls++
	return kernel.UpstreamResponse{Status: http.StatusOK, Body: http.NoBody}, nil
}

func TestAcquireQuotaCredentialLeaseUsesQuotaOnlyAuthority(t *testing.T) {
	flow := &ownerGateCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialLeaseRoute(t, d, connectionID)
	route.QuotaSourceRef = extensions.Ref{Kind: "quota_source", ID: "account-quota", ContractVersion: 1}
	lease, err := d.acquireQuotaCredentialLease(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	quotaScope := kernel.CredentialLeaseScopeForQuota(route)
	if _, err := lease.Borrow(context.Background(), quotaScope); err != nil {
		t.Fatalf("quota borrow failed: %v", err)
	}
	if err := lease.ValidateForDispatch(context.Background(), quotaScope); err != nil {
		t.Fatalf("quota live fence failed: %v", err)
	}
	if _, err := lease.Borrow(context.Background(), kernel.CredentialLeaseScopeForRoute(route)); err == nil {
		t.Fatal("quota lease authorized inference borrowing")
	}
}

func TestQuotaCredentialLeaseGuardRejectsResavedCredential(t *testing.T) {
	flow := &ownerGateCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	route := credentialLeaseRoute(t, d, connectionID)
	route.QuotaSourceRef = extensions.Ref{Kind: "quota_source", ID: "account-quota", ContractVersion: 1}
	lease, err := d.acquireQuotaCredentialLease(context.Background(), route)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := d.store.UpdateConnection(store.UpdateConnectionInput{ID: connectionID, Secret: "original-secret"}); err != nil {
		t.Fatal(err)
	}
	inner := &quotaLeaseTestTransport{}
	body := newIOCloserString("payload")
	_, err = lease.GuardTransport(kernel.CredentialLeaseScopeForQuota(route), inner).Execute(context.Background(), kernel.UpstreamRequest{Body: body})
	if err == nil {
		t.Fatal("final guarded send accepted a credential after its durable revision changed")
	}
	if inner.calls != 0 {
		t.Fatalf("revoked quota lease invoked transport %d times", inner.calls)
	}
	if !body.closed {
		t.Fatal("rejected final send did not close its request body")
	}
}

type ioCloserString struct {
	*strings.Reader
	closed bool
}

func (b *ioCloserString) Close() error { b.closed = true; return nil }

func newIOCloserString(value string) *ioCloserString {
	return &ioCloserString{Reader: strings.NewReader(value)}
}
