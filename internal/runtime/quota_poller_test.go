package runtime

import (
	"context"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type connectionQuotaTestSource struct{ calls int }

func (s *connectionQuotaTestSource) ID() string { return "connection-test" }
func (s *connectionQuotaTestSource) QuotaScope() provider.QuotaScope {
	return provider.QuotaScopeConnection
}
func (s *connectionQuotaTestSource) Fetch(_ context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
	s.calls++
	return []quota.Snapshot{{ProviderNodeID: request.Route.NodeID, ConnectionID: request.Route.CredentialID, WindowName: "account", Source: "test"}}, nil
}

type testQuotaSource struct{}

func (testQuotaSource) ID() string { return "test" }
func (testQuotaSource) Fetch(_ context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
	route := request.Route
	return []quota.Snapshot{{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "test", Source: "test"}}, nil
}

type failingQuotaTestSource struct{ err error }

func (failingQuotaTestSource) ID() string { return "failing-quota" }
func (s failingQuotaTestSource) Fetch(context.Context, provider.QuotaRequest) ([]quota.Snapshot, error) {
	return nil, s.err
}

func TestQuotaPollerResolvesRouteSourceAndRecordsSnapshot(t *testing.T) {
	got := 0
	poller := QuotaPoller{
		Sources:    map[extensions.Ref]provider.QuotaSource{{Kind: "quota_source", ID: "test", ContractVersion: 1}: testQuotaSource{}},
		Endpoints:  map[extensions.Ref]kernel.Endpoint{{Kind: "endpoint", ID: "http-json", ContractVersion: 1}: kernel.HTTPJSONEndpoint{}},
		Transports: map[extensions.Ref]kernel.Transport{{Kind: "transport", ID: "http", ContractVersion: 1}: kernel.HTTPTransport{}},
		Snapshot: func() kernel.Snapshot {
			return kernel.Snapshot{Routes: map[string]kernel.Route{"r": {ID: "r", NodeID: "node", CredentialID: "conn", CredentialType: "api_key", ExternalModel: "model", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "test", ContractVersion: 1}, QuotaEndpointRef: extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}, QuotaTransportRef: extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}, Enabled: true}}}
		},
		AcquireCredential: quotaTestAcquirer("secret", nil),
		Record: func(snapshot quota.Snapshot) {
			got++
			if snapshot.ConnectionID != "conn" {
				t.Fatalf("snapshot=%#v", snapshot)
			}
		},
	}
	poller.poll(context.Background())
	if got != 1 {
		t.Fatalf("recorded=%d", got)
	}
}

func TestQuotaPollerIsOptIn(t *testing.T) {
	called := false
	poller := QuotaPoller{Enabled: false, Record: func(quota.Snapshot) { called = true }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Run(ctx)
	if called {
		t.Fatal("disabled poller should not perform any work")
	}
}

func TestQuotaPollerDeduplicatesConnectionScopedSourcesAcrossModels(t *testing.T) {
	source := &connectionQuotaTestSource{}
	endpointRef := extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}
	transportRef := extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}
	quotaRef := extensions.Ref{Kind: "quota_source", ID: "connection-test", ContractVersion: 1}
	routes := map[string]kernel.Route{}
	for _, model := range []string{"model-a", "model-b"} {
		routes[model] = kernel.Route{ID: model, NodeID: "provider", CredentialID: "connection", ExternalModel: model, QuotaSourceRef: quotaRef, QuotaEndpointRef: endpointRef, QuotaTransportRef: transportRef, Enabled: true}
	}
	var snapshots []quota.Snapshot
	poller := QuotaPoller{
		Sources:           map[extensions.Ref]provider.QuotaSource{quotaRef: source},
		Endpoints:         map[extensions.Ref]kernel.Endpoint{endpointRef: kernel.HTTPJSONEndpoint{}},
		Transports:        map[extensions.Ref]kernel.Transport{transportRef: kernel.HTTPTransport{}},
		Snapshot:          func() kernel.Snapshot { return kernel.Snapshot{Routes: routes} },
		AcquireCredential: quotaTestAcquirer("secret", nil),
		Record:            func(snapshot quota.Snapshot) { snapshots = append(snapshots, snapshot) },
	}
	poller.poll(context.Background())
	if source.calls != 1 || len(snapshots) != 1 || snapshots[0].ConnectionID != "connection" || snapshots[0].ModelRef != "" {
		t.Fatalf("connection-scoped poll calls=%d snapshots=%#v", source.calls, snapshots)
	}
}

func TestQuotaPollerReportsSanitizedProviderFailureWithoutRouteFeedback(t *testing.T) {
	quotaRef := extensions.Ref{Kind: "quota_source", ID: "failing-quota", ContractVersion: 1}
	endpointRef := extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}
	transportRef := extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}
	var stage, code string
	marked := false
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", ExternalModel: "model", Enabled: true, QuotaSourceRef: quotaRef, QuotaEndpointRef: endpointRef, QuotaTransportRef: transportRef}
	poller := QuotaPoller{
		Sources:           map[extensions.Ref]provider.QuotaSource{quotaRef: failingQuotaTestSource{err: &provider.QuotaHTTPError{Status: 403}}},
		Endpoints:         map[extensions.Ref]kernel.Endpoint{endpointRef: kernel.HTTPJSONEndpoint{}},
		Transports:        map[extensions.Ref]kernel.Transport{transportRef: kernel.HTTPTransport{}},
		Snapshot:          func() kernel.Snapshot { return kernel.Snapshot{Routes: map[string]kernel.Route{route.ID: route}} },
		AcquireCredential: quotaTestAcquirer("key", nil),
		Record:            func(quota.Snapshot) { marked = true },
		OnError: func(gotRoute kernel.Route, gotStage, gotCode string) {
			if gotRoute.ID != route.ID {
				t.Errorf("failure was attributed to route %q", gotRoute.ID)
			}
			stage, code = gotStage, gotCode
		},
	}
	poller.poll(context.Background())
	if stage != "fetch" || code != "permission" || marked {
		t.Fatalf("quota failure stage=%q code=%q routeFeedback=%t", stage, code, marked)
	}
}
