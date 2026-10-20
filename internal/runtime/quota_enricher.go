package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// OpportunisticQuota invokes a provider quota primitive only after a real
// request produced quota/rate-limit evidence. It is singleflight per source,
// connection and model, with a cooldown to avoid turning an outage into a
// quota-endpoint storm.
type OpportunisticQuota struct {
	Sources           map[extensions.Ref]provider.QuotaSource
	Endpoints         map[extensions.Ref]kernel.Endpoint
	Transports        map[extensions.Ref]kernel.Transport
	AcquireCredential kernel.CredentialLeaseResolver
	Context           context.Context // daemon lifetime; immutable after the first Trigger
	Record            func(quota.Snapshot)
	OnError           func(kernel.Route, string, string)
	Cooldown          time.Duration
	Timeout           time.Duration
	MaxCooldownKeys   int
	MaxConcurrent     int // best-effort background work; default 16, immutable after first Trigger
	mu                sync.Mutex
	closed            bool
	closeCtx          context.Context
	closeCancel       context.CancelFunc
	workers           sync.WaitGroup
	slots             *semaphore.Weighted
	last              map[quotaRequestIdentity]time.Time
	group             singleflight.Group
}

func (q *OpportunisticQuota) reportFailure(route kernel.Route, stage, code string) {
	if q.OnError != nil {
		q.OnError(route, stage, code)
	}
}

func (q *OpportunisticQuota) Trigger(ctx context.Context, route kernel.Route) {
	if q == nil || route.QuotaSourceRef.ID == "" || q.AcquireCredential == nil || q.Record == nil {
		return
	}
	source := q.Sources[route.QuotaSourceRef]
	if source == nil {
		return
	}
	endpoint := q.Endpoints[route.QuotaEndpointRef]
	transport := q.Transports[route.QuotaTransportRef]
	if endpoint == nil || transport == nil {
		return
	}
	key := quotaRequestKey(route, source)
	cooldown := q.Cooldown
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if q.last == nil {
		q.last = map[quotaRequestIdentity]time.Time{}
	}
	if q.closeCtx == nil {
		q.closeCtx, q.closeCancel = context.WithCancel(context.Background())
	}
	closeCtx := q.closeCtx
	now := time.Now()
	if previous, exists := q.last[key]; exists && now.Sub(previous) < cooldown {
		q.mu.Unlock()
		return
	}
	maxKeys := q.MaxCooldownKeys
	if maxKeys <= 0 {
		maxKeys = 8192
	}
	if _, exists := q.last[key]; !exists && len(q.last) >= maxKeys {
		var oldestKey quotaRequestIdentity
		var hasOldest bool
		var oldest time.Time
		for candidate, touched := range q.last {
			if now.Sub(touched) >= cooldown {
				delete(q.last, candidate)
				continue
			}
			if oldest.IsZero() || touched.Before(oldest) {
				oldestKey, oldest, hasOldest = candidate, touched, true
			}
		}
		if len(q.last) >= maxKeys && hasOldest {
			delete(q.last, oldestKey)
		}
	}
	if q.slots == nil {
		limit := q.MaxConcurrent
		if limit <= 0 {
			limit = 16
		}
		q.slots = semaphore.NewWeighted(int64(limit))
	}
	// Do not queue another goroutine when all background slots are occupied.
	// The signal remains retryable because its cooldown is not advanced.
	if !q.slots.TryAcquire(1) {
		q.mu.Unlock()
		return
	}
	slots := q.slots
	q.last[key] = now
	q.workers.Add(1)
	q.mu.Unlock()
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	// Quota enrichment is deliberately detached from client request lifetime:
	// the caller may disconnect immediately after the upstream 429. It remains
	// bounded so an unresponsive quota endpoint cannot retain work indefinitely.
	go func() {
		defer q.workers.Done()
		defer slots.Release(1)
		q.group.Do(key.singleflightKey(), func() (any, error) {
			base := context.WithoutCancel(ctx)
			background, cancel := context.WithTimeout(base, timeout)
			life := q.Context
			if life == nil {
				life = context.Background()
			}
			stopLife := context.AfterFunc(life, cancel)
			stopClose := context.AfterFunc(closeCtx, cancel)
			defer func() { stopLife(); stopClose(); cancel() }()
			if life.Err() != nil || closeCtx.Err() != nil {
				cancel()
			}
			if background.Err() != nil {
				return nil, background.Err()
			}
			snapshots, requestedAt, err := fetchQuotaWithLease(background, route, source, endpoint, transport, q.AcquireCredential, func(stage, code string) { q.reportFailure(route, stage, code) })
			if err != nil {
				if !errors.Is(err, errQuotaLease) && background.Err() == nil {
					q.reportFailure(route, "fetch", provider.QuotaFailureCode(err))
				}
				return nil, err
			}
			for _, snapshot := range snapshots {
				if snapshot.ProviderNodeID == "" {
					snapshot.ProviderNodeID = route.NodeID
				}
				if snapshot.ConnectionID == "" {
					snapshot.ConnectionID = route.CredentialID
				}
				if provider.QuotaScopeOf(source) == provider.QuotaScopeConnection {
					snapshot.ModelRef = ""
				} else if snapshot.ModelRef == "" {
					snapshot.ModelRef = route.ExternalModel
				}
				snapshot.Generation = route.HealthGeneration.ForQuota(snapshot.ConnectionID, snapshot.ModelRef)
				snapshot.Authority = quota.AuthorityQuotaAPI
				if snapshot.Source == "" {
					snapshot.Source = "quota_api"
				}
				if snapshot.ObservedAt.IsZero() {
					snapshot.ObservedAt = requestedAt
				}
				q.Record(snapshot)
			}
			return nil, nil
		})
	}()
}

// Close stops admission, cancels active detached work, and waits for all
// admitted workers, including workers queued behind singleflight calls.
func (q *OpportunisticQuota) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.closed = true
	if q.closeCtx == nil {
		q.closeCtx, q.closeCancel = context.WithCancel(context.Background())
	}
	cancel := q.closeCancel
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	q.workers.Wait()
}
