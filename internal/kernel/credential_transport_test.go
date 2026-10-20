package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

type leaseTestTransport struct {
	calls atomic.Int32
	fn    func(context.Context, UpstreamRequest) (UpstreamResponse, error)
	name  string
}

func (t *leaseTestTransport) ID() string { return t.name }
func (t *leaseTestTransport) Execute(ctx context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	t.calls.Add(1)
	if t.fn != nil {
		return t.fn(ctx, request)
	}
	return UpstreamResponse{}, nil
}

type leaseTestBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *leaseTestBody) Close() error { b.closed.Store(true); return nil }

func TestCredentialLeaseQuotaScopeIsDistinctAndExact(t *testing.T) {
	lease, operationScope, _ := newLeaseFixture(t, nil, nil)
	quotaScope := operationScope
	quotaScope.OperationRef = extensions.Ref{}
	quotaScope.UseRef = extensions.Ref{Kind: "quota_source", ID: "account-quota", ContractVersion: 3}
	if _, err := lease.Borrow(context.Background(), quotaScope); err == nil {
		t.Fatal("inference lease accepted a quota scope")
	}
	quotaLease, err := NewCredentialLease(quotaScope, Credential{Secret: "quota-secret"}, time.Now().Add(time.Minute), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := quotaLease.Borrow(context.Background(), operationScope); err == nil {
		t.Fatal("quota lease accepted an inference scope")
	}
	if _, err := quotaLease.Borrow(context.Background(), quotaScope); err != nil {
		t.Fatalf("quota scope was rejected: %v", err)
	}
	consumerScope := operationScope
	consumerScope.OperationRef = extensions.Ref{}
	consumerScope.UseRef = extensions.Ref{Kind: "schema", ID: "generic-consumer", ContractVersion: 1}
	if _, err := NewCredentialLease(consumerScope, Credential{Secret: "consumer-secret"}, time.Now().Add(time.Minute), nil, nil); err != nil {
		t.Fatalf("valid generic consumer ref was rejected by the primitive: %v", err)
	}

	cases := map[string]func(*CredentialLeaseScope){
		"both authorities":    func(s *CredentialLeaseScope) { s.OperationRef = operationScope.OperationRef },
		"missing use kind":    func(s *CredentialLeaseScope) { s.UseRef.Kind = "" },
		"missing use ID":      func(s *CredentialLeaseScope) { s.UseRef.ID = "" },
		"invalid use version": func(s *CredentialLeaseScope) { s.UseRef.ContractVersion = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := quotaScope
			mutate(&bad)
			if _, err := NewCredentialLease(bad, Credential{Secret: "x"}, time.Now().Add(time.Minute), nil, nil); err == nil {
				t.Fatal("invalid quota authority was accepted")
			}
		})
	}
}

func TestCredentialLeaseGuardValidatesEverySendAndPreservesResponse(t *testing.T) {
	var revocations atomic.Int32
	lease, scope, _ := newLeaseFixture(t, func(context.Context) error {
		if revocations.Add(1) == 2 {
			return errors.New("revoked")
		}
		return nil
	}, nil)
	responseBody := io.NopCloser(strings.NewReader("raw"))
	response := UpstreamResponse{Status: 207, Headers: http.Header{"X-Raw": []string{"unchanged"}}, Body: responseBody}
	inner := &leaseTestTransport{name: "inner-id", fn: func(context.Context, UpstreamRequest) (UpstreamResponse, error) { return response, nil }}
	guard := lease.GuardTransport(scope, inner)
	if guard.ID() != inner.ID() {
		t.Fatalf("guard ID = %q, want delegate ID %q", guard.ID(), inner.ID())
	}
	got, err := guard.Execute(context.Background(), UpstreamRequest{})
	if err != nil || got.Body != response.Body || got.Status != response.Status || got.Headers.Get("X-Raw") != "unchanged" {
		t.Fatalf("guard changed raw response: got=%#v err=%v", got, err)
	}
	body := &leaseTestBody{Reader: strings.NewReader("request")}
	if _, err := guard.Execute(context.Background(), UpstreamRequest{Body: body}); err == nil {
		t.Fatal("revoked send succeeded")
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("inner transport calls = %d, want 1", inner.calls.Load())
	}
	if !body.closed.Load() {
		t.Fatal("rejected request body was not closed")
	}
}

func TestCredentialLeaseGuardFailsClosedAndDoesNotHoldLeaseLock(t *testing.T) {
	lease, scope, _ := newLeaseFixture(t, nil, nil)
	body := &leaseTestBody{Reader: strings.NewReader("request")}
	if _, err := lease.GuardTransport(scope, nil).Execute(context.Background(), UpstreamRequest{Body: body}); err == nil || !body.closed.Load() {
		t.Fatal("nil transport did not fail closed and close request body")
	}
	var nilLease *CredentialLease
	body = &leaseTestBody{Reader: strings.NewReader("request")}
	inner := &leaseTestTransport{name: "inner"}
	if _, err := nilLease.GuardTransport(scope, inner).Execute(context.Background(), UpstreamRequest{Body: body}); err == nil || !body.closed.Load() {
		t.Fatal("nil lease did not fail closed and close request body")
	}

	calledRelease := make(chan struct{})
	inner.fn = func(context.Context, UpstreamRequest) (UpstreamResponse, error) {
		lease.Release()
		close(calledRelease)
		return UpstreamResponse{Status: 200}, nil
	}
	if _, err := lease.GuardTransport(scope, inner).Execute(context.Background(), UpstreamRequest{}); err != nil {
		t.Fatalf("inner transport could not release lease during Execute: %v", err)
	}
	select {
	case <-calledRelease:
	default:
		t.Fatal("inner transport was not called")
	}
}

func TestCredentialLeaseGuardHonorsExpiryAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope, credential := leaseFixture()
		lease, err := NewCredentialLease(scope, credential, time.Now().Add(time.Second), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		inner := &leaseTestTransport{name: "inner"}
		guard := lease.GuardTransport(scope, inner)
		if _, err := guard.Execute(context.Background(), UpstreamRequest{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if _, err := guard.Execute(context.Background(), UpstreamRequest{}); err == nil {
			t.Fatal("expired lease reached transport")
		}
		if inner.calls.Load() != 1 {
			t.Fatalf("expired dispatch invoked inner transport; calls=%d", inner.calls.Load())
		}
	})
	synctest.Test(t, func(t *testing.T) {
		lease, scope, _ := newLeaseFixture(t, nil, nil)
		inner := &leaseTestTransport{name: "inner"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		body := &leaseTestBody{Reader: strings.NewReader("request")}
		if _, err := lease.GuardTransport(scope, inner).Execute(ctx, UpstreamRequest{Body: body}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled guarded dispatch error = %v", err)
		}
		if inner.calls.Load() != 0 || !body.closed.Load() {
			t.Fatal("canceled dispatch reached inner transport or left body open")
		}
	})
}

func TestCredentialLeaseGuardDoesNotCloseResponseAfterExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope, credential := leaseFixture()
		lease, err := NewCredentialLease(scope, credential, time.Now().Add(time.Second), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		responseBody := io.NopCloser(strings.NewReader("response"))
		inner := &leaseTestTransport{name: "inner", fn: func(context.Context, UpstreamRequest) (UpstreamResponse, error) {
			return UpstreamResponse{Body: responseBody}, nil
		}}
		response, err := lease.GuardTransport(scope, inner).Execute(context.Background(), UpstreamRequest{})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if _, err := io.ReadAll(response.Body); err != nil {
			t.Fatalf("lease expiry interfered with returned response body: %v", err)
		}
	})
}
