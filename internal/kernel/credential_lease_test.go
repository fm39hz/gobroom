package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func leaseFixture() (CredentialLeaseScope, Credential) {
	return CredentialLeaseScope{
		RouteID: "route-a", ProviderNodeID: "provider-a", ConnectionID: "connection-a",
		CredentialType: "oauth", AuthFlowID: "oauth-flow", BaseURL: "https://upstream.invalid",
		ModelRef: "model-a", PrincipalGeneration: "principal-v1",
		DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "provider-a", ContractVersion: 1},
		Protocol:      ProtocolOpenAIChat,
		OperationRef:  extensions.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 1},
	}, Credential{Generation: "credential-private-r7", ConnectionID: "connection-a", Type: "oauth", Secret: "secret-value",
		RequestHeaders: http.Header{"X-Auth-Extra": []string{"header-secret"}}, AuthContext: AuthContext{"vendor.user": "user-secret"}}
}

func newLeaseFixture(t *testing.T, check func(context.Context) error, release func()) (*CredentialLease, CredentialLeaseScope, Credential) {
	t.Helper()
	scope, credential := leaseFixture()
	lease, err := NewCredentialLease(scope, credential, time.Now().Add(time.Minute), check, release)
	if err != nil {
		t.Fatal(err)
	}
	return lease, scope, credential
}

func TestCredentialLeaseCopiesRuntimeMaterial(t *testing.T) {
	lease, scope, input := newLeaseFixture(t, nil, nil)
	input.RequestHeaders.Set("X-Auth-Extra", "mutated")
	input.AuthContext["vendor.user"] = "mutated"
	borrowed, err := lease.Borrow(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if borrowed.RequestHeaders.Get("X-Auth-Extra") != "header-secret" || borrowed.AuthContext["vendor.user"] != "user-secret" {
		t.Fatal("constructor did not isolate runtime maps")
	}
	borrowed.RequestHeaders.Set("X-Auth-Extra", "borrow-mutated")
	borrowed.AuthContext["vendor.user"] = "borrow-mutated"
	second, err := lease.Borrow(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if second.RequestHeaders.Get("X-Auth-Extra") != "header-secret" || second.AuthContext["vendor.user"] != "user-secret" {
		t.Fatal("borrowed maps alias lease material")
	}
}

func TestCredentialLeaseRejectsEveryBindingMismatch(t *testing.T) {
	lease, scope, _ := newLeaseFixture(t, nil, nil)
	cases := map[string]func(*CredentialLeaseScope){
		"route":                func(s *CredentialLeaseScope) { s.RouteID += "-other" },
		"provider":             func(s *CredentialLeaseScope) { s.ProviderNodeID += "-other" },
		"connection":           func(s *CredentialLeaseScope) { s.ConnectionID += "-other" },
		"credential type":      func(s *CredentialLeaseScope) { s.CredentialType += "-other" },
		"auth flow":            func(s *CredentialLeaseScope) { s.AuthFlowID += "-other" },
		"base URL":             func(s *CredentialLeaseScope) { s.BaseURL += "/other" },
		"model":                func(s *CredentialLeaseScope) { s.ModelRef += "-other" },
		"principal generation": func(s *CredentialLeaseScope) { s.PrincipalGeneration += "-other" },
		"definition ref":       func(s *CredentialLeaseScope) { s.DefinitionRef.ContractVersion++ },
		"protocol":             func(s *CredentialLeaseScope) { s.Protocol = ProtocolGemini },
		"operation ID":         func(s *CredentialLeaseScope) { s.OperationRef.ID += ".other" },
		"operation version":    func(s *CredentialLeaseScope) { s.OperationRef.ContractVersion++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			wrong := scope
			mutate(&wrong)
			if _, err := lease.Borrow(context.Background(), wrong); err == nil {
				t.Fatal("expected scope mismatch")
			}
		})
	}
}

func TestCredentialLeaseConstructorValidatesScopeAndMaterialBinding(t *testing.T) {
	scope, credential := leaseFixture()
	for name, mutate := range map[string]func(*CredentialLeaseScope, *Credential){
		"connection":     func(s *CredentialLeaseScope, c *Credential) { c.ConnectionID = "another-connection" },
		"type":           func(s *CredentialLeaseScope, c *Credential) { c.Type = "another-type" },
		"operation kind": func(s *CredentialLeaseScope, c *Credential) { s.OperationRef.Kind = "schema" },
	} {
		t.Run(name, func(t *testing.T) {
			s, c := scope, credential
			mutate(&s, &c)
			if _, err := NewCredentialLease(s, c, time.Now().Add(time.Minute), nil, nil); err == nil {
				t.Fatal("expected constructor rejection")
			}
		})
	}
	stateless := CredentialLeaseScope{RouteID: "route", OperationRef: extensions.Ref{Kind: "operation", ID: "op", ContractVersion: 1}}
	standalone := Credential{Secret: "standalone"}
	if _, err := NewCredentialLease(stateless, standalone, time.Now().Add(time.Minute), nil, nil); err != nil {
		t.Fatalf("stateless fixture rejected: %v", err)
	}
}

func TestCredentialLeaseExpiryRefreshAndDispatchFence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope, credential := leaseFixture()
		credential.ExpiresAt = time.Now().Add(time.Second)
		var checks atomic.Int32
		lease, err := NewCredentialLease(scope, credential, time.Now().Add(30*time.Minute), func(context.Context) error { checks.Add(1); return nil }, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := lease.ExpiresAt(); !got.Equal(credential.ExpiresAt) {
			t.Fatalf("expiry was not capped: %v", got)
		}
		if err := lease.ValidateForDispatch(context.Background(), scope); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if _, err := lease.Borrow(context.Background(), scope); err == nil {
			t.Fatal("expired lease allowed dispatch borrow")
		}
		material, err := lease.MaterialForRefresh(context.Background(), scope)
		if err != nil || material.Secret != credential.Secret {
			t.Fatalf("expired lease refresh material: %q, %v", material.Secret, err)
		}
		if err := lease.ValidateForDispatch(context.Background(), scope); err == nil {
			t.Fatal("escaped refresh material revived dispatch rights")
		}
		if checks.Load() != 1 {
			t.Fatalf("live check count = %d, want 1", checks.Load())
		}
	})
}

func TestCredentialLeaseLiveRevocationAndCheckScopeOrdering(t *testing.T) {
	var calls atomic.Int32
	revoked := errors.New("revoked")
	lease, scope, _ := newLeaseFixture(t, func(context.Context) error { calls.Add(1); return revoked }, nil)
	wrong := scope
	wrong.ConnectionID = "wrong"
	if err := lease.ValidateForDispatch(context.Background(), wrong); err == nil {
		t.Fatal("wrong scope accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("live check ran for wrong scope")
	}
	if err := lease.ValidateForDispatch(context.Background(), scope); !errors.Is(err, revoked) {
		t.Fatalf("revocation error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("live checks = %d", calls.Load())
	}
}

func TestCredentialLeaseReleaseOnceUnderConcurrency(t *testing.T) {
	var calls atomic.Int32
	lease, _, _ := newLeaseFixture(t, nil, func() { calls.Add(1) })
	var group sync.WaitGroup
	for i := 0; i < 64; i++ {
		group.Add(1)
		go func() { defer group.Done(); lease.Release() }()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("release callback count = %d", calls.Load())
	}
	if _, err := lease.Borrow(context.Background(), lease.Scope()); err == nil {
		t.Fatal("released lease remained borrowable")
	}
}

func TestCredentialLeasePrivacyAndAccessors(t *testing.T) {
	lease, _, _ := newLeaseFixture(t, nil, nil)
	for _, text := range []string{fmt.Sprint(lease), fmt.Sprintf("%#v", lease), fmt.Sprint(lease.Scope()), fmt.Sprintf("%#v", lease.Scope())} {
		if strings.Contains(text, "secret") || strings.Contains(text, "credential-private") || strings.Contains(text, "principal-v1") || strings.Contains(text, "connection-a") {
			t.Fatalf("sensitive data leaked in formatter: %s", text)
		}
	}
	for _, value := range []any{lease, lease.Scope()} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "secret") || strings.Contains(text, "credential-private") || strings.Contains(text, "principal-v1") || strings.Contains(text, "connection-a") {
			t.Fatalf("sensitive data leaked in JSON: %s", text)
		}
	}
	if lease.Generation() != "credential-private-r7" || lease.Scope().ConnectionID != "connection-a" {
		t.Fatal("private accessors returned unexpected values")
	}
}

func TestCredentialLeaseNilAndInvalidExpiryFailClosed(t *testing.T) {
	var nilLease *CredentialLease
	if _, err := nilLease.Borrow(context.Background(), CredentialLeaseScope{}); err == nil {
		t.Fatal("nil lease borrowed")
	}
	if err := nilLease.ValidateForDispatch(context.Background(), CredentialLeaseScope{}); err == nil {
		t.Fatal("nil lease validated")
	}
	nilLease.Release()
	scope, credential := leaseFixture()
	for _, expiry := range []time.Time{time.Now(), time.Now().Add(MaxCredentialLeaseDuration + time.Second)} {
		if _, err := NewCredentialLease(scope, credential, expiry, nil, nil); err == nil {
			t.Fatalf("accepted expiry %v", expiry)
		}
	}
}
