package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	"golang.org/x/sync/singleflight"
)

// OpportunisticQuota invokes a provider quota primitive only after a real
// request produced quota/rate-limit evidence. It is singleflight per source,
// connection and model, with a cooldown to avoid turning an outage into a
// quota-endpoint storm.
type OpportunisticQuota struct {
	Sources         map[extensions.Ref]provider.QuotaSource
	Endpoints       map[extensions.Ref]kernel.Endpoint
	Transports      map[extensions.Ref]kernel.Transport
	Credential      func(context.Context, kernel.Route) (kernel.Credential, error)
	Record          func(quota.Snapshot)
	Cooldown        time.Duration
	Timeout         time.Duration
	MaxCooldownKeys int
	mu              sync.Mutex
	last            map[string]time.Time
	group           singleflight.Group
}

func (q *OpportunisticQuota) Trigger(ctx context.Context, route kernel.Route) {
	if q == nil || route.QuotaSourceRef.ID == "" || q.Credential == nil || q.Record == nil {
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
	key := route.QuotaSourceRef.Key() + "\x00" + route.CredentialID + "\x00" + route.ExternalModel
	cooldown := q.Cooldown
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	q.mu.Lock()
	if q.last == nil {
		q.last = map[string]time.Time{}
	}
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
		var oldestKey string
		var oldest time.Time
		for candidate, touched := range q.last {
			if now.Sub(touched) >= cooldown {
				delete(q.last, candidate)
				continue
			}
			if oldest.IsZero() || touched.Before(oldest) {
				oldestKey, oldest = candidate, touched
			}
		}
		if len(q.last) >= maxKeys && oldestKey != "" {
			delete(q.last, oldestKey)
		}
	}
	q.last[key] = now
	q.mu.Unlock()
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	// Quota enrichment is deliberately detached from client request lifetime:
	// the caller may disconnect immediately after the upstream 429. It remains
	// bounded so an unresponsive quota endpoint cannot retain work indefinitely.
	q.group.DoChan(key, func() (any, error) {
		background, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		credential, err := q.Credential(background, route)
		if err != nil {
			return nil, err
		}
		snapshots, err := source.Fetch(background, provider.QuotaRequest{Credential: credential, Route: route, Endpoint: endpoint, Transport: transport, EndpointOptions: route.QuotaEndpointOptions, WindowName: route.QuotaWindowName})
		if err != nil {
			return nil, err
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
		return nil, nil
	})
}
