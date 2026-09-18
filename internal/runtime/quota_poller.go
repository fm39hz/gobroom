package runtime

import (
	"context"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type QuotaPoller struct {
	Sources    map[string]provider.QuotaSource
	Snapshot   func() kernel.Snapshot
	Credential func(context.Context, kernel.Route) (kernel.Credential, error)
	Record     func(quota.Snapshot)
	Interval   time.Duration
}

func (p QuotaPoller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	p.poll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p QuotaPoller) poll(ctx context.Context) {
	if p.Snapshot == nil || p.Credential == nil || p.Record == nil {
		return
	}
	seen := map[string]bool{}
	for _, route := range p.Snapshot().Routes {
		if route.QuotaSourceID == "" || route.CredentialID == "" {
			continue
		}
		source := p.Sources[route.QuotaSourceID]
		if source == nil {
			continue
		}
		key := route.QuotaSourceID + "\x00" + route.CredentialID + "\x00" + route.ExternalModel
		if seen[key] {
			continue
		}
		seen[key] = true
		credential, err := p.Credential(ctx, route)
		if err != nil {
			continue
		}
		snapshots, err := source.Fetch(ctx, credential, route)
		if err != nil {
			continue
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
			p.Record(snapshot)
		}
	}
}
