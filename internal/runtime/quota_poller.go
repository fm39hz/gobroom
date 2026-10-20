package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type QuotaPoller struct {
	Sources           map[extensions.Ref]provider.QuotaSource
	Endpoints         map[extensions.Ref]kernel.Endpoint
	Transports        map[extensions.Ref]kernel.Transport
	Snapshot          func() kernel.Snapshot
	AcquireCredential kernel.CredentialLeaseResolver
	Record            func(quota.Snapshot)
	OnError           func(kernel.Route, string, string)
	Interval          time.Duration
	// Enabled is opt-in. Runtime quota evidence normally comes from real
	// attempts; polling is reserved for explicitly configured providers.
	Enabled bool
}

type quotaRequestIdentity struct {
	ProviderNodeID      string
	ConnectionID        string
	SourceRef           extensions.Ref
	PrincipalGeneration string
	CredentialRevision  string
	ModelRef            string
}

func quotaRequestKey(route kernel.Route, source provider.QuotaSource) quotaRequestIdentity {
	key := quotaRequestIdentity{
		ProviderNodeID: route.NodeID, ConnectionID: route.CredentialID, SourceRef: route.QuotaSourceRef,
		PrincipalGeneration: route.HealthGeneration.Connection, CredentialRevision: route.HealthGeneration.Credential,
	}
	if provider.QuotaScopeOf(source) == provider.QuotaScopeModel {
		key.ModelRef = route.ExternalModel
	}
	return key
}

func (key quotaRequestIdentity) singleflightKey() string {
	encoded, _ := json.Marshal(key)
	return string(encoded)
}

func (p QuotaPoller) reportFailure(route kernel.Route, stage, code string) {
	if p.OnError != nil {
		p.OnError(route, stage, code)
	}
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
	if ctx.Err() != nil || p.Snapshot == nil || p.AcquireCredential == nil || p.Record == nil {
		return
	}
	seen := map[quotaRequestIdentity]bool{}
	for _, route := range p.Snapshot().Routes {
		if route.QuotaSourceRef.ID == "" || route.CredentialID == "" {
			continue
		}
		source := p.Sources[route.QuotaSourceRef]
		if source == nil {
			p.reportFailure(route, "source", "unavailable")
			continue
		}
		key := quotaRequestKey(route, source)
		if seen[key] {
			continue
		}
		seen[key] = true
		endpoint := p.Endpoints[route.QuotaEndpointRef]
		transport := p.Transports[route.QuotaTransportRef]
		if endpoint == nil || transport == nil {
			p.reportFailure(route, "binding", "unavailable")
			continue
		}
		snapshots, requestedAt, err := fetchQuotaWithLease(ctx, route, source, endpoint, transport, p.AcquireCredential, func(stage, code string) { p.reportFailure(route, stage, code) })
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, errQuotaLease) {
				p.reportFailure(route, "fetch", provider.QuotaFailureCode(err))
			}
			continue
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
			p.Record(snapshot)
		}
	}
}
