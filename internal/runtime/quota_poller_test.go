package runtime

import (
	"context"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type testQuotaSource struct{}

func (testQuotaSource) ID() string { return "test" }
func (testQuotaSource) Fetch(_ context.Context, _ kernel.Credential, route kernel.Route) ([]quota.Snapshot, error) {
	return []quota.Snapshot{{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "test", Source: "test"}}, nil
}

func TestQuotaPollerResolvesRouteSourceAndRecordsSnapshot(t *testing.T) {
	got := 0
	poller := QuotaPoller{
		Sources: map[string]provider.QuotaSource{"test": testQuotaSource{}},
		Snapshot: func() kernel.Snapshot {
			return kernel.Snapshot{Routes: map[string]kernel.Route{"r": {NodeID: "node", CredentialID: "conn", ExternalModel: "model", QuotaSourceID: "test", Enabled: true}}}
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
