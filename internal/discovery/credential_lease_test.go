package discovery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type leasePaginationSource struct {
	requests atomic.Int32
}

func (*leasePaginationSource) ID() string { return "lease-pagination-fixture" }

func (s *leasePaginationSource) List(ctx context.Context, input provider.ModelSourceInput) ([]provider.ModelDescriptor, error) {
	_, err := s.ListSnapshot(ctx, input)
	return nil, err
}

func (s *leasePaginationSource) ListSnapshot(ctx context.Context, input provider.ModelSourceInput) (provider.ModelCatalogSnapshot, error) {
	for range 2 {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
		if err != nil {
			return provider.ModelCatalogSnapshot{}, err
		}
		response, err := input.Client.Do(request)
		if err != nil {
			return provider.ModelCatalogSnapshot{}, err
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			return provider.ModelCatalogSnapshot{}, readErr
		}
		if closeErr != nil {
			return provider.ModelCatalogSnapshot{}, closeErr
		}
		s.requests.Add(1)
	}
	return provider.ModelCatalogSnapshot{Models: []provider.ModelDescriptor{{ID: "model-a"}}, Complete: true}, nil
}

func TestDiscoveryMissingLeaseIssuerFailsClosedBeforeHTTP(t *testing.T) {
	state, node, connection, bindings := newDiscoveryLeaseFixture(t)
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}
	service := Service{Store: state, Client: client, Bindings: bindings}
	if _, err := service.TestConnection(context.Background(), node.ID, connection.ID); err == nil {
		t.Fatal("discovery succeeded without a credential lease issuer")
	}
	if requests.Load() != 0 {
		t.Fatalf("missing issuer made %d HTTP requests", requests.Load())
	}
}

func TestDiscoveryRevisionChangeStopsPaginationAndImport(t *testing.T) {
	state, node, connection, bindings := newDiscoveryLeaseFixture(t)
	modelSource := &leasePaginationSource{}
	binding := bindings[provider.RuntimeBindingKey(node.DefinitionRef(), provider.OperationModels)]
	binding.ModelSource = modelSource
	bindings[provider.RuntimeBindingKey(node.DefinitionRef(), provider.OperationModels)] = binding
	credential, found := state.ConnectionCredentialByID(connection.ID)
	if !found {
		t.Fatal("fixture credential missing")
	}
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			if _, err := state.CompareAndSwapConnectionCredential(context.Background(), credential, "replacement-secret", store.CredentialRenewal); err != nil {
				t.Errorf("resave credential during first page: %v", err)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}
	service := Service{Store: state, Client: client, Bindings: bindings, AcquireCredential: testLeaseIssuer(state, bindings)}
	if _, err := service.RefreshConnection(context.Background(), node.ID, connection.ID); err == nil {
		t.Fatal("catalog import succeeded after its credential revision changed")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests after revision change = %d, want only the first page", got)
	}
	if got := modelSource.requests.Load(); got != 1 {
		t.Fatalf("accepted pages = %d, want 1", got)
	}
	models, err := state.Models()
	if err != nil || len(models) != 0 {
		t.Fatalf("stale catalog was imported: models=%#v err=%v", models, err)
	}
}

func TestDiscoveryRejectsWrongScopeAndReleasesLeaseOnce(t *testing.T) {
	state, node, connection, bindings := newDiscoveryLeaseFixture(t)
	var requests, releases atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}
	issuer := func(_ context.Context, route kernel.Route, scope kernel.CredentialLeaseScope) (*kernel.CredentialLease, error) {
		wrongScope := scope
		wrongScope.ModelRef = "wrong-model"
		material, err := (provider.StaticSecretAuth{}).Resolve(context.Background(), provider.AuthInput{ConnectionID: connection.ID, Type: "api_key", Secret: "test-secret"})
		if err != nil {
			return nil, err
		}
		return kernel.NewCredentialLease(wrongScope, material, time.Now().Add(time.Minute), nil, func() { releases.Add(1) })
	}
	service := Service{Store: state, Client: client, Bindings: bindings, AcquireCredential: issuer}
	if _, err := service.TestConnection(context.Background(), node.ID, connection.ID); err == nil {
		t.Fatal("discovery accepted a lease for the wrong model scope")
	}
	if requests.Load() != 0 {
		t.Fatalf("wrong-scope lease made %d HTTP requests", requests.Load())
	}
	if releases.Load() != 1 {
		t.Fatalf("wrong-scope lease releases = %d, want 1", releases.Load())
	}
}

func newDiscoveryLeaseFixture(t *testing.T) (*store.Store, store.ProviderNode, store.ConnectionRecord, map[string]provider.RuntimeBinding) {
	t.Helper()
	state, err := store.Open(t.TempDir() + "/discovery-lease.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	return state, node, connection, bindings
}
