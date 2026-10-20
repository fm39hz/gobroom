package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

func quotaTestAcquirer(secret string, released *atomic.Int32) kernel.CredentialLeaseResolver {
	return func(_ context.Context, route kernel.Route) (*kernel.CredentialLease, error) {
		material := kernel.Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: secret}
		lease, err := kernel.NewCredentialLease(kernel.CredentialLeaseScopeForQuota(route), material, time.Now().Add(time.Minute), nil, func() {
			if released != nil {
				released.Add(1)
			}
		})
		return lease, err
	}
}

type quotaFuncSource func(context.Context, provider.QuotaRequest) ([]quota.Snapshot, error)

func (quotaFuncSource) ID() string { return "lease-test" }
func (s quotaFuncSource) Fetch(ctx context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
	return s(ctx, request)
}

type quotaFuncTransport func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error)

func (quotaFuncTransport) ID() string { return "lease-test" }
func (f quotaFuncTransport) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	return f(ctx, request)
}

func TestQuotaLeaseRejectsMutationBeforeTransportAndDiscardsPartialSnapshots(t *testing.T) {
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", CredentialType: "api_key", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}}
	var executed atomic.Int32
	var revoked atomic.Bool
	acquire := func(_ context.Context, route kernel.Route) (*kernel.CredentialLease, error) {
		return kernel.NewCredentialLease(kernel.CredentialLeaseScopeForQuota(route), kernel.Credential{ConnectionID: route.CredentialID, Type: route.CredentialType, Secret: "secret"}, time.Now().Add(time.Minute), func(context.Context) error {
			if revoked.Load() {
				return errors.New("revoked")
			}
			return nil
		}, nil)
	}
	transport := quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
		executed.Add(1)
		return kernel.UpstreamResponse{}, nil
	})
	source := quotaFuncSource(func(_ context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
		revoked.Store(true)
		_, err := request.Transport.Execute(context.Background(), kernel.UpstreamRequest{})
		if err == nil {
			t.Fatal("guard accepted mutated route authority")
		}
		return []quota.Snapshot{{WindowName: "partial"}}, nil
	})
	snapshots, _, err := fetchQuotaWithLease(context.Background(), route, source, kernel.HTTPJSONEndpoint{}, transport, acquire, nil)
	if err == nil || snapshots != nil || executed.Load() != 0 {
		t.Fatalf("stale result accepted: snapshots=%#v err=%v executes=%d", snapshots, err, executed.Load())
	}
}

func TestOpportunisticQuotaBoundsAdmittedWorkersWithoutCoolingDroppedSignal(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	source := quotaFuncSource(func(ctx context.Context, _ provider.QuotaRequest) ([]quota.Snapshot, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	})
	ref := extensions.Ref{Kind: "quota_source", ID: "bounded", ContractVersion: 1}
	ep := extensions.Ref{Kind: "endpoint", ID: "http-json", ContractVersion: 1}
	tx := extensions.Ref{Kind: "transport", ID: "http", ContractVersion: 1}
	var acquired atomic.Int32
	q := &OpportunisticQuota{
		MaxConcurrent: 1,
		Sources:       map[extensions.Ref]provider.QuotaSource{ref: source},
		Endpoints:     map[extensions.Ref]kernel.Endpoint{ep: kernel.HTTPJSONEndpoint{}},
		Transports:    map[extensions.Ref]kernel.Transport{tx: kernel.HTTPTransport{}},
		AcquireCredential: func(ctx context.Context, route kernel.Route) (*kernel.CredentialLease, error) {
			acquired.Add(1)
			return quotaTestAcquirer("secret", nil)(ctx, route)
		}, Record: func(quota.Snapshot) {},
	}
	defer q.Close()
	first := kernel.Route{ID: "first", ExternalModel: "first", CredentialID: "connection", QuotaSourceRef: ref, QuotaEndpointRef: ep, QuotaTransportRef: tx}
	second := first
	second.ID, second.ExternalModel = "second", "second"
	q.Trigger(context.Background(), first)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first quota read did not start")
	}
	q.Trigger(context.Background(), second)
	if acquired.Load() != 1 {
		t.Fatal("capacity overflow admitted a second credential")
	}
	// Release only the admitted fetch, wait for its worker, then retry the signal
	// that was dropped at capacity. No concurrent Trigger runs during this wait.
	q.mu.Lock()
	if _, exists := q.last[quotaRequestKey(second, source)]; exists {
		t.Error("dropped signal was cooldowned")
	}
	q.mu.Unlock()
	select {
	case release <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("first fetch did not accept release")
	}
	q.workers.Wait()
	q.Trigger(context.Background(), second)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dropped signal could not retry after capacity became free")
	}
	if acquired.Load() != 2 {
		t.Fatal("retry did not acquire its own lease")
	}
}

func TestQuotaLeaseStopsSecondTransportAndDiscardsPartialSnapshots(t *testing.T) {
	route := kernel.Route{ID: "route", NodeID: "provider", CredentialID: "connection", CredentialType: "api_key", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}}
	var executed atomic.Int32
	var sourceErr error
	var lease *kernel.CredentialLease
	acquire := func(ctx context.Context, r kernel.Route) (*kernel.CredentialLease, error) {
		var err error
		lease, err = quotaTestAcquirer("secret", nil)(ctx, r)
		return lease, err
	}
	transport := quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
		if executed.Add(1) == 1 {
			lease.Release()
		}
		return kernel.UpstreamResponse{}, sourceErr
	})
	source := quotaFuncSource(func(ctx context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
		if _, err := request.Transport.Execute(ctx, kernel.UpstreamRequest{}); err != nil {
			return []quota.Snapshot{{WindowName: "partial"}}, nil
		}
		_, err := request.Transport.Execute(ctx, kernel.UpstreamRequest{})
		if err != nil {
			return []quota.Snapshot{{WindowName: "partial"}}, nil
		}
		return []quota.Snapshot{{WindowName: "partial"}}, nil
	})
	snapshots, _, err := fetchQuotaWithLease(context.Background(), route, source, kernel.HTTPJSONEndpoint{}, transport, acquire, nil)
	if err == nil || snapshots != nil || executed.Load() != 1 {
		t.Fatalf("partial result accepted: snapshots=%#v err=%v executes=%d", snapshots, err, executed.Load())
	}
}

func TestQuotaLeaseReleasesAfterFetchSuccessAndFailure(t *testing.T) {
	for _, fetchErr := range []error{nil, errors.New("fetch failed")} {
		var released atomic.Int32
		route := kernel.Route{ID: "route", CredentialID: "connection", CredentialType: "api_key", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}}
		source := quotaFuncSource(func(context.Context, provider.QuotaRequest) ([]quota.Snapshot, error) { return nil, fetchErr })
		_, _, _ = fetchQuotaWithLease(context.Background(), route, source, kernel.HTTPJSONEndpoint{}, quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
			return kernel.UpstreamResponse{}, nil
		}), quotaTestAcquirer("secret", &released), nil)
		if released.Load() != 1 {
			t.Fatalf("released=%d after fetch error %v", released.Load(), fetchErr)
		}
	}
	var released atomic.Int32
	route := kernel.Route{ID: "route-error-lease", CredentialID: "connection", CredentialType: "api_key", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}}
	acquireErr := errors.New("issuer unavailable")
	acquire := func(context.Context, kernel.Route) (*kernel.CredentialLease, error) {
		lease, err := quotaTestAcquirer("secret", &released)(context.Background(), route)
		if err != nil {
			return lease, err
		}
		return lease, acquireErr
	}
	_, _, gotErr := fetchQuotaWithLease(context.Background(), route, quotaFuncSource(func(context.Context, provider.QuotaRequest) ([]quota.Snapshot, error) {
		t.Fatal("source ran after acquisition returned an error")
		return nil, nil
	}), kernel.HTTPJSONEndpoint{}, quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
		return kernel.UpstreamResponse{}, nil
	}), acquire, nil)
	if !errors.Is(gotErr, acquireErr) || released.Load() != 1 {
		t.Fatalf("non-nil lease returned with error was not released: err=%v releases=%d", gotErr, released.Load())
	}
}

func TestQuotaLeaseExpiryStopsDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		route := kernel.Route{ID: "route", CredentialID: "connection", CredentialType: "api_key", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}}
		acquire := func(context.Context, kernel.Route) (*kernel.CredentialLease, error) {
			return kernel.NewCredentialLease(kernel.CredentialLeaseScopeForQuota(route), kernel.Credential{ConnectionID: route.CredentialID, Type: route.CredentialType}, time.Now().Add(5*time.Second), nil, nil)
		}
		source := quotaFuncSource(func(ctx context.Context, request provider.QuotaRequest) ([]quota.Snapshot, error) {
			time.Sleep(10 * time.Second)
			_, err := request.Transport.Execute(ctx, kernel.UpstreamRequest{})
			return nil, err
		})
		snapshots, _, err := fetchQuotaWithLease(context.Background(), route, source, kernel.HTTPJSONEndpoint{}, quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
			return kernel.UpstreamResponse{}, nil
		}), acquire, nil)
		if err == nil || snapshots != nil {
			t.Fatalf("expired lease accepted: snapshots=%#v err=%v", snapshots, err)
		}
	})
}

func TestQuotaRequestKeyFencesPrincipalAndRevisionButSharesConnectionModels(t *testing.T) {
	source := &connectionQuotaTestSource{}
	base := kernel.Route{NodeID: "provider", CredentialID: "connection", QuotaSourceRef: extensions.Ref{Kind: "quota_source", ID: "source", ContractVersion: 1}, ExternalModel: "model-a", HealthGeneration: kernel.RouteHealthGeneration{Connection: "principal-1", Credential: "revision-1"}}
	modelB := base
	modelB.ExternalModel = "model-b"
	if quotaRequestKey(base, source) != quotaRequestKey(modelB, source) {
		t.Fatal("connection-scoped quota key unexpectedly includes model")
	}
	principal := base
	principal.HealthGeneration.Connection = "principal-2"
	revision := base
	revision.HealthGeneration.Credential = "revision-2"
	if quotaRequestKey(base, source) == quotaRequestKey(principal, source) || quotaRequestKey(base, source) == quotaRequestKey(revision, source) {
		t.Fatal("quota key did not fence principal or credential revision")
	}
}

func TestOpportunisticQuotaLifetimeCancelsDetachedRequest(t *testing.T) {
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	started := make(chan struct{})
	finished := make(chan struct{})
	source := quotaFuncSource(func(ctx context.Context, _ provider.QuotaRequest) ([]quota.Snapshot, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	})
	ref := extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}
	enricher := &OpportunisticQuota{
		Context:   lifetime,
		Sources:   map[extensions.Ref]provider.QuotaSource{ref: source},
		Endpoints: map[extensions.Ref]kernel.Endpoint{{Kind: "endpoint", ID: "ep", ContractVersion: 1}: kernel.HTTPJSONEndpoint{}},
		Transports: map[extensions.Ref]kernel.Transport{{Kind: "transport", ID: "tx", ContractVersion: 1}: quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
			return kernel.UpstreamResponse{}, nil
		})},
		AcquireCredential: quotaTestAcquirer("secret", nil), Record: func(quota.Snapshot) {},
	}
	route := kernel.Route{ID: "life-route", CredentialID: "conn", CredentialType: "api_key", QuotaSourceRef: ref, QuotaEndpointRef: extensions.Ref{Kind: "endpoint", ID: "ep", ContractVersion: 1}, QuotaTransportRef: extensions.Ref{Kind: "transport", ID: "tx", ContractVersion: 1}}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	enricher.Trigger(requestCtx, route)
	cancelRequest()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("detached source did not start")
	}
	cancelLifetime()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("daemon lifetime cancellation did not interrupt source")
	}
	enricher.Close()
}

func TestOpportunisticQuotaCloseDrainsAndRejectsNewWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		finished := make(chan struct{})
		source := quotaFuncSource(func(ctx context.Context, _ provider.QuotaRequest) ([]quota.Snapshot, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil, ctx.Err()
		})
		ref := extensions.Ref{Kind: "quota_source", ID: "lease-test", ContractVersion: 1}
		var acquired atomic.Int32
		acquire := func(ctx context.Context, route kernel.Route) (*kernel.CredentialLease, error) {
			acquired.Add(1)
			return quotaTestAcquirer("secret", nil)(ctx, route)
		}
		enricher := &OpportunisticQuota{
			Cooldown:  time.Nanosecond,
			Sources:   map[extensions.Ref]provider.QuotaSource{ref: source},
			Endpoints: map[extensions.Ref]kernel.Endpoint{{Kind: "endpoint", ID: "ep", ContractVersion: 1}: kernel.HTTPJSONEndpoint{}},
			Transports: map[extensions.Ref]kernel.Transport{{Kind: "transport", ID: "tx", ContractVersion: 1}: quotaFuncTransport(func(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
				return kernel.UpstreamResponse{}, nil
			})},
			AcquireCredential: acquire, Record: func(quota.Snapshot) {},
		}
		route := kernel.Route{ID: "close-route", CredentialID: "conn", CredentialType: "api_key", QuotaSourceRef: ref, QuotaEndpointRef: extensions.Ref{Kind: "endpoint", ID: "ep", ContractVersion: 1}, QuotaTransportRef: extensions.Ref{Kind: "transport", ID: "tx", ContractVersion: 1}}
		enricher.Trigger(context.Background(), route)
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("source did not start")
		}
		time.Sleep(time.Nanosecond)                   // advance the virtual cooldown clock
		enricher.Trigger(context.Background(), route) // admitted and queued behind the same singleflight call
		synctest.Wait()
		drained := make(chan struct{})
		go func() { enricher.Close(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(time.Second):
			t.Fatal("Close did not cancel and drain admitted worker")
		}
		if acquired.Load() != 1 {
			t.Fatalf("admitted request acquired %d credentials", acquired.Load())
		}
		enricher.Trigger(context.Background(), route)
		if acquired.Load() != 1 {
			t.Fatal("Trigger admitted work after Close")
		}
		select {
		case <-finished:
		default:
			t.Fatal("source did not finish during Close")
		}
	})
}
