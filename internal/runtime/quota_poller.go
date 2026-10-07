package runtime

import (
	"context"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type QuotaPoller struct {
	Sources    map[extensions.Ref]provider.QuotaSource
	Endpoints  map[extensions.Ref]kernel.Endpoint
	Transports map[extensions.Ref]kernel.Transport
	Snapshot   func() kernel.Snapshot
	Credential func(context.Context, kernel.Route) (kernel.Credential, error)
	Record     func(quota.Snapshot)
	Interval   time.Duration
	// Enabled is opt-in. Runtime quota evidence normally comes from real
	// attempts; polling is reserved for explicitly configured providers.
	Enabled bool
}

func (p QuotaPoller) Run(ctx context.Context) {
	if !p.Enabled {
		return
	}
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
		if route.QuotaSourceRef.ID == "" || route.CredentialID == "" {
			continue
		}
		source := p.Sources[route.QuotaSourceRef]
		if source == nil {
			continue
		}
		key := route.QuotaSourceRef.Key() + "\x00" + route.CredentialID + "\x00" + route.ExternalModel
		if seen[key] {
			continue
		}
		seen[key] = true
		credential, err := p.Credential(ctx, route)
		if err != nil {
			continue
		}
		endpoint := p.Endpoints[route.QuotaEndpointRef]
		transport := p.Transports[route.QuotaTransportRef]
		if endpoint == nil || transport == nil {
			continue
		}
		snapshots, err := source.Fetch(ctx, provider.QuotaRequest{Credential: credential, Route: route, Endpoint: endpoint, Transport: transport, EndpointOptions: route.QuotaEndpointOptions, WindowName: route.QuotaWindowName})
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
