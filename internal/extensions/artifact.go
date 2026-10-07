package extensions

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const ArtifactKind = "artifact"

type ArtifactReplayScope string

const (
	ArtifactReplayRequest    ArtifactReplayScope = "request"
	ArtifactReplaySession    ArtifactReplayScope = "session"
	ArtifactReplayConnection ArtifactReplayScope = "connection"
	ArtifactReplayProvider   ArtifactReplayScope = "provider"
	ArtifactReplayPortable   ArtifactReplayScope = "portable"
)

type ArtifactSensitivity string

const (
	ArtifactPublic  ArtifactSensitivity = "public"
	ArtifactPrivate ArtifactSensitivity = "private"
	ArtifactSecret  ArtifactSensitivity = "secret"
)

type ArtifactOwner struct {
	Domain               string `json:"domain"`
	IssuerRef            *Ref   `json:"issuerRef,omitempty"`
	ProviderDefinitionID string `json:"providerDefinitionId,omitempty"`
	ConnectionID         string `json:"connectionId,omitempty"`
	ModelIdentity        string `json:"modelIdentity,omitempty"`
	ClientContract       string `json:"clientContract,omitempty"`
	SessionID            string `json:"sessionId,omitempty"`
}

type BodyRef struct {
	ID         string    `json:"id"`
	MediaType  string    `json:"mediaType"`
	SizeBytes  int64     `json:"sizeBytes"`
	SHA256     string    `json:"sha256,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	Replayable bool      `json:"replayable"`
}

// ArtifactRef carries provenance and replay semantics with either a small
// inline JSON value or a bounded external body lease. The body bytes never
// appear in the invocation envelope.
type ArtifactRef struct {
	TypeRef     Ref                 `json:"typeRef"`
	Role        string              `json:"role,omitempty"`
	Owner       ArtifactOwner       `json:"owner"`
	ReplayScope ArtifactReplayScope `json:"replayScope"`
	Sensitivity ArtifactSensitivity `json:"sensitivity"`
	MediaType   string              `json:"mediaType"`
	SizeBytes   int64               `json:"sizeBytes"`
	SHA256      string              `json:"sha256,omitempty"`
	ExpiresAt   time.Time           `json:"expiresAt,omitempty"`
	Value       []byte              `json:"value,omitempty"`
	Body        *BodyRef            `json:"body,omitempty"`
}

// ArtifactPolicy is attached to one exact artifact type descriptor.
// Recipient contracts are explicit; a portable label alone does not authorize
// cross-provider transfer.
type ArtifactPolicy struct {
	OwnerDomains       []string              `json:"ownerDomains"`
	ReplayScopes       []ArtifactReplayScope `json:"replayScopes"`
	SensitivityLimit   ArtifactSensitivity   `json:"sensitivityLimit"`
	AllowedMediaTypes  []string              `json:"allowedMediaTypes,omitempty"`
	RecipientContracts []Ref                 `json:"recipientContracts,omitempty"`
}

func (a ArtifactRef) Clone() ArtifactRef {
	a.Value = append([]byte(nil), a.Value...)
	if a.Owner.IssuerRef != nil {
		issuer := *a.Owner.IssuerRef
		a.Owner.IssuerRef = &issuer
	}
	if a.Body != nil {
		body := *a.Body
		a.Body = &body
	}
	return a
}

func (p ArtifactPolicy) AllowsScope(scope ArtifactReplayScope) bool {
	for _, candidate := range p.ReplayScopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

func (p ArtifactPolicy) AllowsOwnerDomain(domain string) bool {
	for _, candidate := range p.OwnerDomains {
		if candidate == domain {
			return true
		}
	}
	return false
}

func (p ArtifactPolicy) AllowsMediaType(mediaType string) bool {
	if len(p.AllowedMediaTypes) == 0 {
		return true
	}
	for _, candidate := range p.AllowedMediaTypes {
		if candidate == mediaType {
			return true
		}
	}
	return false
}

func sensitivityRank(value ArtifactSensitivity) int {
	switch value {
	case ArtifactPublic:
		return 0
	case ArtifactPrivate:
		return 1
	case ArtifactSecret:
		return 2
	default:
		return -1
	}
}

func (a ArtifactRef) ValidateShape(maxBytes int64) error {
	if err := a.TypeRef.Validate(); err != nil || a.TypeRef.Kind != ArtifactKind {
		return fmt.Errorf("artifact requires an exact %q type ref", ArtifactKind)
	}
	if strings.TrimSpace(a.Owner.Domain) == "" {
		return fmt.Errorf("artifact owner domain is required")
	}
	if a.Owner.IssuerRef != nil {
		if err := a.Owner.IssuerRef.Validate(); err != nil {
			return fmt.Errorf("artifact issuer: %w", err)
		}
	}
	if !validArtifactReplayScope(a.ReplayScope) || sensitivityRank(a.Sensitivity) < 0 {
		return fmt.Errorf("artifact has invalid replay scope or sensitivity")
	}
	if a.MediaType == "" || a.SizeBytes < 0 {
		return fmt.Errorf("artifact requires mediaType and non-negative sizeBytes")
	}
	if maxBytes > 0 && a.SizeBytes > maxBytes {
		return fmt.Errorf("artifact size %d exceeds type limit %d", a.SizeBytes, maxBytes)
	}
	if (len(a.Value) > 0) == (a.Body != nil) {
		return fmt.Errorf("artifact must contain exactly one inline value or body reference")
	}
	if len(a.Value) > 0 && int64(len(a.Value)) != a.SizeBytes {
		return fmt.Errorf("inline artifact size mismatch: declared %d bytes, actual %d", a.SizeBytes, len(a.Value))
	}
	if a.Body != nil {
		if a.Body.ID == "" || a.Body.SizeBytes != a.SizeBytes || a.Body.MediaType != a.MediaType {
			return fmt.Errorf("artifact body reference metadata does not match the artifact")
		}
		if a.ReplayScope != ArtifactReplayRequest && !a.Body.Replayable {
			return fmt.Errorf("artifact body is not replayable at scope %q", a.ReplayScope)
		}
		if !a.Body.ExpiresAt.IsZero() && !a.ExpiresAt.IsZero() && a.Body.ExpiresAt.Before(a.ExpiresAt) {
			return fmt.Errorf("body lease expires before its artifact")
		}
		if a.Body.SHA256 != "" && !validSHA256(a.Body.SHA256) {
			return fmt.Errorf("body reference has invalid SHA-256 digest")
		}
		if a.SHA256 != "" && a.Body.SHA256 != "" && a.SHA256 != a.Body.SHA256 {
			return fmt.Errorf("artifact and body reference digests do not match")
		}
	}
	if a.SHA256 != "" && !validSHA256(a.SHA256) {
		return fmt.Errorf("artifact has invalid SHA-256 digest")
	}
	return nil
}

func validArtifactReplayScope(scope ArtifactReplayScope) bool {
	switch scope {
	case ArtifactReplayRequest, ArtifactReplaySession, ArtifactReplayConnection, ArtifactReplayProvider, ArtifactReplayPortable:
		return true
	default:
		return false
	}
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateArtifactPolicy(policy ArtifactPolicy) error {
	if len(policy.OwnerDomains) == 0 || len(policy.ReplayScopes) == 0 || sensitivityRank(policy.SensitivityLimit) < 0 {
		return fmt.Errorf("artifact policy requires owner domains, replay scopes and a valid sensitivity limit")
	}
	seenDomains := map[string]bool{}
	for _, domain := range policy.OwnerDomains {
		if strings.TrimSpace(domain) == "" || seenDomains[domain] {
			return fmt.Errorf("artifact policy has an empty or duplicate owner domain")
		}
		seenDomains[domain] = true
	}
	seenScopes := map[ArtifactReplayScope]bool{}
	for _, scope := range policy.ReplayScopes {
		if !validArtifactReplayScope(scope) || seenScopes[scope] {
			return fmt.Errorf("artifact policy has an invalid or duplicate replay scope %q", scope)
		}
		seenScopes[scope] = true
	}
	seenMedia := map[string]bool{}
	for _, mediaType := range policy.AllowedMediaTypes {
		if strings.TrimSpace(mediaType) == "" || seenMedia[mediaType] {
			return fmt.Errorf("artifact policy has an empty or duplicate media type")
		}
		seenMedia[mediaType] = true
	}
	seenRecipients := map[Ref]bool{}
	for _, recipient := range policy.RecipientContracts {
		if err := recipient.Validate(); err != nil || seenRecipients[recipient] {
			return fmt.Errorf("artifact policy has an invalid or duplicate recipient contract")
		}
		seenRecipients[recipient] = true
	}
	return nil
}

func cloneArtifactPolicy(policy ArtifactPolicy) ArtifactPolicy {
	policy.OwnerDomains = append([]string(nil), policy.OwnerDomains...)
	policy.ReplayScopes = append([]ArtifactReplayScope(nil), policy.ReplayScopes...)
	policy.AllowedMediaTypes = append([]string(nil), policy.AllowedMediaTypes...)
	policy.RecipientContracts = append([]Ref(nil), policy.RecipientContracts...)
	return policy
}

func artifactOwnersEqual(left, right ArtifactOwner) bool {
	if left.Domain != right.Domain || left.ProviderDefinitionID != right.ProviderDefinitionID || left.ConnectionID != right.ConnectionID || left.ModelIdentity != right.ModelIdentity || left.ClientContract != right.ClientContract || left.SessionID != right.SessionID {
		return false
	}
	if left.IssuerRef == nil || right.IssuerRef == nil {
		return left.IssuerRef == nil && right.IssuerRef == nil
	}
	return *left.IssuerRef == *right.IssuerRef
}

func containsRef(refs []Ref, wanted Ref) bool {
	for _, ref := range refs {
		if ref == wanted {
			return true
		}
	}
	return false
}
