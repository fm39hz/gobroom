package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type countingQuotaSource struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (s *countingQuotaSource) ID() string { return "counting" }
func (s *countingQuotaSource) Fetch(_ context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
	route := request.Route
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.started != nil {
		s.started <- struct{}{}
		select {
		case <-s.release:
		case <-time.After(time.Second):
			return nil, context.DeadlineExceeded
		}
	}
	return []quota.Snapshot{{ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, ModelRef: route.ExternalModel, WindowName: "minute"}}, nil
}

func TestOpportunisticQuotaIsSingleflightAndCooldowned(t *testing.T) {
	source := &countingQuotaSource{started: make(chan struct{}, 2), release: make(chan struct{})}
	recorded := make(chan quota.Snapshot, 2)
	enricher := &OpportunisticQuota{
		Sources:    map[extensions.Ref]provider.QuotaSource{{Kind: "quota_source", ID: "counting", ContractVersion: 1}: source},
		Endpoints:  map[extensions.Ref]kernel.Endpoint{{Kind: "endpoint", ID: "http-json", ContractVersion: 1}: kernel.HTTPJSONEndpoint{}},
		Transports: map[extensions.Ref]kernel.Transport{{Kind: "transport", ID: "http", ContractVersion: 1}: kernel.HTTPTransport{}},
		Cooldown:   time.Nanosecond,
		Timeout:    time.Second,
		Credential: func(ctx context.Context, _ kernel.Route) (kernel.Credential, error) {
			if ctx.Err() != nil {
				t.Errorf("quota enrichment inherited canceled request context: %v", ctx.Err())
			}
			return kernel.Credential{Secret: "x"}, nil
		},
		Record: func(snapshot quota.Snapshot) { recorded <- snapshot },
	}
	route := kernel.Route{QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "counting", ContractVersion: 1}, QuotaEndpointRef: extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}, QuotaTransportRef: extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}, CredentialID: "conn", ExternalModel: "model"}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	enricher.Trigger(requestCtx, route)
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("quota source was not called")
	}
	// A near-immediate repeat bypasses the cooldown while the first source call
	// is still blocked, so this assertion exercises singleflight itself.
	enricher.Trigger(context.Background(), route)
	time.Sleep(time.Millisecond)
	close(source.release)
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

func TestOpportunisticQuotaBoundsCooldownKeys(t *testing.T) {
	source := &countingQuotaSource{}
	recorded := make(chan quota.Snapshot, 2)
	enricher := &OpportunisticQuota{
		Sources:         map[extensions.Ref]provider.QuotaSource{{Kind: "quota_source", ID: "counting", ContractVersion: 1}: source},
		Endpoints:       map[extensions.Ref]kernel.Endpoint{{Kind: "endpoint", ID: "http-json", ContractVersion: 1}: kernel.HTTPJSONEndpoint{}},
		Transports:      map[extensions.Ref]kernel.Transport{{Kind: "transport", ID: "http", ContractVersion: 1}: kernel.HTTPTransport{}},
		Cooldown:        time.Hour,
		MaxCooldownKeys: 1,
		Credential: func(context.Context, kernel.Route) (kernel.Credential, error) {
			return kernel.Credential{Secret: "x"}, nil
		},
		Record: func(snapshot quota.Snapshot) { recorded <- snapshot },
	}
	base := kernel.Route{QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "counting", ContractVersion: 1}, QuotaEndpointRef: extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}, QuotaTransportRef: extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}, CredentialID: "conn"}
	first := base
	first.ExternalModel = "model-a"
	second := base
	second.ExternalModel = "model-b"
	enricher.Trigger(context.Background(), first)
	enricher.Trigger(context.Background(), second)
	for range 2 {
		select {
		case <-recorded:
		case <-time.After(time.Second):
			t.Fatal("bounded cooldown cache prevented a distinct route from being enriched")
		}
	}
	source.mu.Lock()
	calls := source.calls
	source.mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected both distinct models to be enriched, got %d calls", calls)
	}
	enricher.mu.Lock()
	defer enricher.mu.Unlock()
	if len(enricher.last) != 1 {
		t.Fatalf("cooldown key cache exceeded configured bound: got %d keys", len(enricher.last))
	}
}
