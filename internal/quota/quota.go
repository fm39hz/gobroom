package quota

import "time"

// Snapshot is routing state, not merely display telemetry. A scheduler can
// reject a candidate when Remaining is zero until ResetAt, or prefer a route
// with more remaining budget.
type Snapshot struct {
	ProviderNodeID string
	ConnectionID   string
	ModelRef       string
	WindowName     string
	Used           float64
	Limit          *float64
	Remaining      *float64
	ResetAt        *time.Time
	Source         string
}

func (s Snapshot) Exhausted(now time.Time) bool {
	if s.Remaining == nil || *s.Remaining > 0 {
		return false
	}
	return s.ResetAt == nil || s.ResetAt.After(now)
}

func (s Snapshot) Usable(now time.Time) bool { return !s.Exhausted(now) }

// Gate is intentionally small so provider-specific quota APIs can feed the
// scheduler without coupling the scheduler to a provider implementation.
type Gate interface {
	Snapshot(providerNodeID, connectionID, modelRef, window string) (Snapshot, bool)
	Record(snapshot Snapshot)
}
