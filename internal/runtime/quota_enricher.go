package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

// OpportunisticQuota invokes a provider quota primitive only after a real
// request produced quota/rate-limit evidence. It is singleflight per source,
// connection and model, with a cooldown to avoid turning an outage into a
// quota-endpoint storm.
type OpportunisticQuota struct {
	Sources    map[string]provider.QuotaSource
	Credential func(context.Context, kernel.Route) (kernel.Credential, error)
	Record     func(quota.Snapshot)
	Cooldown   time.Duration
	mu         sync.Mutex
	active     map[string]bool
	last       map[string]time.Time
}

func (q *OpportunisticQuota) Trigger(ctx context.Context, route kernel.Route) {
	if q == nil || route.QuotaSourceID == "" || q.Credential == nil || q.Record == nil {
		return
	}
	source := q.Sources[route.QuotaSourceID]
	if source == nil {
		return
	}
	key := route.QuotaSourceID + "\x00" + route.CredentialID + "\x00" + route.ExternalModel
	cooldown := q.Cooldown
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	q.mu.Lock()
	if q.active == nil {
		q.active = map[string]bool{}
	}
	if q.last == nil {
		q.last = map[string]time.Time{}
	}
	if q.active[key] || time.Since(q.last[key]) < cooldown {
		q.mu.Unlock()
		return
	}
	q.active[key] = true
	q.last[key] = time.Now()
	q.mu.Unlock()
	go func() {
		defer func() { q.mu.Lock(); delete(q.active, key); q.mu.Unlock() }()
		credential, err := q.Credential(ctx, route)
		if err != nil {
			return
		}
		snapshots, err := source.Fetch(ctx, credential, route)
		if err != nil {
			return
		}
		for _, snapshot := range snapshots {
			if snapshot.ProviderNodeID == "" {
				snapshot.ProviderNodeID = route.NodeID
			}
			if snapshot.ConnectionID == "" {
				snapshot.ConnectionID = route.CredentialID
			}
			if snapshot.ModelRef == "" {
				snapshot.ModelRef = route.ExternalModel
			}
			q.Record(snapshot)
		}
	}()
}
