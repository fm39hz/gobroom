package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type countingQuotaSource struct {
	mu    sync.Mutex
	calls int
}

func (s *countingQuotaSource) ID() string { return "counting" }
func (s *countingQuotaSource) Fetch(_ context.Context, _ kernel.Credential, route kernel.Route) ([]quota.Snapshot, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return []quota.Snapshot{{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "minute"}}, nil
}

func TestOpportunisticQuotaIsSingleflightAndCooldowned(t *testing.T) {
	source := &countingQuotaSource{}
	recorded := make(chan quota.Snapshot, 2)
	enricher := &OpportunisticQuota{
		Sources:  map[string]provider.QuotaSource{"counting": source},
		Cooldown: time.Hour,
		Credential: func(context.Context, kernel.Route) (kernel.Credential, error) {
			return kernel.Credential{Secret: "x"}, nil
		},
		Record: func(snapshot quota.Snapshot) { recorded <- snapshot },
	}
	route := kernel.Route{QuotaSourceID: "counting", CredentialID: "conn", ExternalModel: "model"}
	enricher.Trigger(context.Background(), route)
	enricher.Trigger(context.Background(), route)
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("enrichment did not complete")
	}
	time.Sleep(10 * time.Millisecond)
	source.mu.Lock()
	calls := source.calls
	source.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected one quota call, got %d", calls)
	}
}
