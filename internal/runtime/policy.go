package runtime

import (
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/quota"
)

type PolicyGate struct {
	Health *HealthGate
	mu     sync.RWMutex
	quotas map[string]quota.Snapshot
}

func NewPolicyGate() *PolicyGate {
	return &PolicyGate{Health: NewHealthGate(), quotas: map[string]quota.Snapshot{}}
}
func (g *PolicyGate) Usable(route kernel.Route, now time.Time) bool {
	if !g.Health.Usable(route, now) {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, snapshot := range g.quotas {
		if snapshot.ProviderNodeID == route.NodeID && (snapshot.ModelRef == "" || snapshot.ModelRef == route.ExternalModel) && (snapshot.ConnectionID == "" || snapshot.ConnectionID == route.CredentialID) && snapshot.Exhausted(now) {
			return false
		}
	}
	return true
}
func (g *PolicyGate) MarkFailure(route kernel.Route, class kernel.ErrorClass, err error) {
	g.Health.MarkFailure(route, class, err)
}
func (g *PolicyGate) MarkSuccess(route kernel.Route) { g.Health.MarkSuccess(route) }
func (g *PolicyGate) SetQuota(snapshot quota.Snapshot) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.quotas[quotaKey(snapshot)] = snapshot
}
func (g *PolicyGate) Quotas() map[string]quota.Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make(map[string]quota.Snapshot, len(g.quotas))
	for key, value := range g.quotas {
		result[key] = value
	}
	return result
}
func quotaKey(snapshot quota.Snapshot) string {
	return snapshot.ProviderNodeID + "\x00" + snapshot.ConnectionID + "\x00" + snapshot.ModelRef + "\x00" + snapshot.WindowName
}
