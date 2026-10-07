package runtime

import (
	"context"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type testQuotaSource struct{}

func (testQuotaSource) ID() string { return "test" }
func (testQuotaSource) Fetch(_ context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
	route := request.Route
	return []quota.Snapshot{{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "test", Source: "test"}}, nil
}

func TestQuotaPollerResolvesRouteSourceAndRecordsSnapshot(t *testing.T) {
	got := 0
	poller := QuotaPoller{
		Sources:    map[extensions.Ref]provider.QuotaSource{{Kind: "quota_source", ID: "test", ContractVersion: 1}: testQuotaSource{}},
		Endpoints:  map[extensions.Ref]kernel.Endpoint{{Kind: "endpoint", ID: "http-json", ContractVersion: 1}: kernel.HTTPJSONEndpoint{}},
		Transports: map[extensions.Ref]kernel.Transport{{Kind: "transport", ID: "http", ContractVersion: 1}: kernel.HTTPTransport{}},
		Snapshot: func() kernel.Snapshot {
			return kernel.Snapshot{Routes: map[string]kernel.Route{"r": {NodeID: "node", CredentialID: "conn", ExternalModel: "model", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "test", ContractVersion: 1}, QuotaEndpointRef: extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}, QuotaTransportRef: extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}, Enabled: true}}}
		},
		Credential: func(context.Context, kernel.Route) (kernel.Credential, error) {
			return kernel.Credential{Secret: "secret"}, nil
		},
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
