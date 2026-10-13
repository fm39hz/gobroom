package quota

import (
	"encoding/json"
	"fmt"
	"time"
)

type EvidenceAuthority uint8

const (
	AuthorityUnknown EvidenceAuthority = iota
	AuthorityInferred
	AuthorityQuotaAPI
	AuthorityErrorBody
	AuthoritySuccessBody
	AuthorityResponseHeader
	AuthorityExplicit
)

// Snapshot is routing state, not merely display telemetry. A scheduler can
// reject a candidate when Remaining is zero until ResetAt, or prefer a route
// with more remaining budget.
type Snapshot struct {
	ProviderNodeID string
	ConnectionID   string
	ModelRef       string
	WindowName     string
	Generation     string            `json:"-"`
	Authority      EvidenceAuthority `json:"authority,omitempty"`
	ObservedAt     time.Time         `json:"observedAt,omitempty"`
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

func (a EvidenceAuthority) String() string {
	switch a {
	case AuthorityInferred:
		return "inferred"
	case AuthorityQuotaAPI:
		return "quota_api"
	case AuthorityErrorBody:
		return "error_body"
	case AuthoritySuccessBody:
		return "success_body"
	case AuthorityResponseHeader:
		return "response_header"
	case AuthorityExplicit:
		return "explicit"
	default:
		return "unknown"
	}
}

func (a EvidenceAuthority) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

func (a *EvidenceAuthority) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch value {
	case "unknown":
		*a = AuthorityUnknown
	case "inferred":
		*a = AuthorityInferred
	case "quota_api":
		*a = AuthorityQuotaAPI
	case "error_body":
		*a = AuthorityErrorBody
	case "success_body":
		*a = AuthoritySuccessBody
	case "response_header":
		*a = AuthorityResponseHeader
	case "explicit":
		*a = AuthorityExplicit
	default:
		return fmt.Errorf("unknown quota evidence authority %q", value)
	}
	return nil
}

func AuthorityFromSource(source string) EvidenceAuthority {
	switch source {
	case "response_header":
		return AuthorityResponseHeader
	case "success_body":
		return AuthoritySuccessBody
	case "error_body":
		return AuthorityErrorBody
	case "inferred":
		return AuthorityInferred
	case "quota_api", "http-json-quota":
		return AuthorityQuotaAPI
	default:
		return AuthorityUnknown
	}
}

// MergeSnapshot resolves competing observations for the same typed window.
// Freshness is primary; authority provides deterministic tie-breaking.
func MergeSnapshot(current, incoming Snapshot) Snapshot {
	if incoming.ObservedAt.After(current.ObservedAt) {
		return mergeKnownWindowFields(current, incoming)
	}
	if incoming.ObservedAt.Before(current.ObservedAt) {
		return current
	}
	if incoming.Authority > current.Authority {
		return mergeKnownWindowFields(current, incoming)
	}
	if incoming.Authority < current.Authority {
		return current
	}
	if incoming.Source >= current.Source {
		return mergeKnownWindowFields(current, incoming)
	}
	return current
}

func mergeKnownWindowFields(current, incoming Snapshot) Snapshot {
	if incoming.Limit == nil {
		incoming.Limit = current.Limit
	}
	if incoming.Remaining == nil {
		incoming.Remaining = current.Remaining
	}
	if incoming.ResetAt == nil {
		incoming.ResetAt = current.ResetAt
	}
	return incoming
}

// Gate is intentionally small so provider-specific quota APIs can feed the
// scheduler without coupling the scheduler to a provider implementation.
type Gate interface {
	Snapshot(providerNodeID, connectionID, modelRef, window string) (Snapshot, bool)
	Record(snapshot Snapshot)
}
