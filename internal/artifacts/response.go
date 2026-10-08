package artifacts

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

type responseScopeKey struct{}

// ResponseScope owns provider-created artifact leases for one response. The
// provider is the source owner; the operation/client pair is the only allowed
// recipient. Close releases every body even when decode or rendering fails.
type ResponseScope struct {
	store     *Store
	catalog   *extensions.Snapshot
	owner     extensions.ArtifactOwner
	receiver  extensions.ArtifactOwner
	recipient extensions.Ref
	outputs   map[string]extensions.ArtifactOutput
	maxBytes  int64

	mu        sync.Mutex
	closed    bool
	used      int64
	pending   int64
	counts    map[string]int
	artifacts []extensions.ArtifactRef
	allowlist *BodyAllowlist
}

func NewResponseScope(store *Store, catalog *extensions.Snapshot, owner, receiver extensions.ArtifactOwner, recipient extensions.Ref, outputs []extensions.ArtifactOutput, maxBytes int64) (*ResponseScope, error) {
	if store == nil || catalog == nil || owner.Domain == "" || receiver.Domain == "" || maxBytes <= 0 {
		return nil, fmt.Errorf("response artifact scope requires store, catalog, owners and positive output bound")
	}
	if err := recipient.Validate(); err != nil || recipient.Kind != "operation" {
		return nil, fmt.Errorf("response artifact recipient must be an exact operation contract")
	}
	if owner.Domain != "provider" {
		return nil, fmt.Errorf("response artifact source owner must be a provider")
	}
	ports := make(map[string]extensions.ArtifactOutput, len(outputs))
	for _, output := range outputs {
		if strings.TrimSpace(output.Role) == "" || output.TypeRef.Kind != extensions.ArtifactKind || output.MaxCount <= 0 || output.MinCount < 0 || output.MinCount > output.MaxCount {
			return nil, fmt.Errorf("response artifact scope contains an invalid output port")
		}
		if _, exists := ports[output.Role]; exists {
			return nil, fmt.Errorf("response artifact scope contains duplicate output role %q", output.Role)
		}
		ports[output.Role] = output
	}
	return &ResponseScope{store: store, catalog: catalog, owner: cloneOwner(owner), receiver: cloneOwner(receiver), recipient: recipient, outputs: ports, maxBytes: maxBytes, counts: make(map[string]int, len(ports)), allowlist: newBodyAllowlist()}, nil
}

func WithResponseScope(ctx context.Context, scope *ResponseScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if scope == nil {
		return ctx
	}
	return context.WithValue(WithStore(ctx, scope.store), responseScopeKey{}, scope)
}

// PutResponseArtifactFromContext streams provider output into the daemon's
// bounded spool and validates the declared type's transfer policy before the
// reference becomes visible to a response decoder.
func PutResponseArtifactFromContext(ctx context.Context, typeRef extensions.Ref, role, mediaType string, sensitivity extensions.ArtifactSensitivity, source io.Reader, maxBytes int64, ttl time.Duration) (extensions.ArtifactRef, error) {
	if ctx == nil || source == nil {
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact context and source are required")
	}
	if strings.TrimSpace(role) == "" || strings.TrimSpace(mediaType) == "" {
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact role and media type are required")
	}
	scope, ok := ctx.Value(responseScopeKey{}).(*ResponseScope)
	if !ok || scope == nil {
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact scope is unavailable")
	}
	descriptor, ok := scope.catalog.Descriptor(typeRef)
	if !ok || typeRef.Kind != extensions.ArtifactKind || descriptor.ArtifactPolicy == nil {
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact type %q is not registered with an artifact policy", typeRef.Key())
	}
	port, declared := scope.outputs[role]
	if !declared || port.TypeRef != typeRef {
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact role/type %q/%q is not declared by the operation", role, typeRef.Key())
	}
	policy := *descriptor.ArtifactPolicy
	if !policy.AllowsOwnerDomain(scope.owner.Domain) || !policy.AllowsScope(extensions.ArtifactReplayRequest) {
		return extensions.ArtifactRef{}, fmt.Errorf("artifact type %q does not permit provider-owned response output", typeRef.Key())
	}
	if sensitivityRank(sensitivity) > sensitivityRank(policy.SensitivityLimit) || !policy.AllowsMediaType(mediaType) {
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact sensitivity or media type violates %q policy", typeRef.Key())
	}
	preflight := extensions.ArtifactRef{
		TypeRef: typeRef, Role: role, Owner: cloneOwner(scope.owner), ReplayScope: extensions.ArtifactReplayRequest,
		Sensitivity: sensitivity, MediaType: mediaType, Body: &extensions.BodyRef{ID: "preflight", MediaType: mediaType},
	}
	if err := scope.catalog.ValidateArtifact(preflight, scope.receiver, scope.recipient, time.Now()); err != nil {
		return extensions.ArtifactRef{}, err
	}
	if maxBytes <= 0 || maxBytes > descriptor.ResourceBounds.MaxInputBytes {
		maxBytes = descriptor.ResourceBounds.MaxInputBytes
	}
	scope.mu.Lock()
	if scope.closed {
		scope.mu.Unlock()
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact scope is closed")
	}
	if scope.counts[role]+1 > port.MaxCount {
		scope.mu.Unlock()
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact role %q exceeds maximum count %d", role, port.MaxCount)
	}
	remaining := scope.maxBytes - scope.used - scope.pending
	if remaining <= 0 {
		scope.mu.Unlock()
		return extensions.ArtifactRef{}, fmt.Errorf("response artifacts exceed %d-byte operation output limit", scope.maxBytes)
	}
	scope.counts[role]++
	if maxBytes > remaining {
		maxBytes = remaining
	}
	scope.pending += maxBytes
	scope.mu.Unlock()
	body, err := scope.store.Put(ctx, scope.owner, mediaType, source, maxBytes, ttl, false)
	scope.mu.Lock()
	scope.pending -= maxBytes
	if err != nil {
		scope.counts[role]--
		scope.mu.Unlock()
		return extensions.ArtifactRef{}, err
	}
	if scope.closed {
		scope.counts[role]--
		scope.mu.Unlock()
		_ = scope.store.Release(scope.owner, body)
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact scope is closed")
	}
	scope.used += body.SizeBytes
	scope.mu.Unlock()
	artifact := extensions.ArtifactRef{
		TypeRef: typeRef, Role: role, Owner: cloneOwner(scope.owner), ReplayScope: extensions.ArtifactReplayRequest,
		Sensitivity: sensitivity, MediaType: mediaType, SizeBytes: body.SizeBytes, SHA256: body.SHA256,
		ExpiresAt: body.ExpiresAt, Body: &body,
	}
	if err := scope.catalog.ValidateArtifact(artifact, scope.receiver, scope.recipient, time.Now()); err != nil {
		scope.mu.Lock()
		scope.used -= body.SizeBytes
		scope.counts[role]--
		scope.mu.Unlock()
		_ = scope.store.Release(scope.owner, body)
		return extensions.ArtifactRef{}, err
	}
	scope.mu.Lock()
	if scope.closed {
		scope.used -= body.SizeBytes
		scope.counts[role]--
		scope.mu.Unlock()
		_ = scope.store.Release(scope.owner, body)
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact scope is closed")
	}
	scope.artifacts = append(scope.artifacts, artifact.Clone())
	if artifact.Body != nil && !scope.allowlist.add(artifact) {
		scope.artifacts = scope.artifacts[:len(scope.artifacts)-1]
		scope.used -= body.SizeBytes
		scope.counts[role]--
		scope.mu.Unlock()
		_ = scope.store.Release(scope.owner, body)
		return extensions.ArtifactRef{}, fmt.Errorf("response artifact scope is closed")
	}
	scope.mu.Unlock()
	return artifact, nil
}

func (s *ResponseScope) Access() Access {
	if s == nil {
		return Access{}
	}
	return Access{Store: s.store, Catalog: s.catalog, Receiver: cloneOwner(s.receiver), Recipient: s.recipient, Allowlist: s.allowlist}
}

func (s *ResponseScope) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.allowlist.close()
	created := append([]extensions.ArtifactRef(nil), s.artifacts...)
	s.artifacts = nil
	s.mu.Unlock()
	var firstErr error
	for _, artifact := range created {
		if artifact.Body == nil {
			continue
		}
		if err := s.store.Release(s.owner, *artifact.Body); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func sensitivityRank(value extensions.ArtifactSensitivity) int {
	switch value {
	case extensions.ArtifactPublic:
		return 0
	case extensions.ArtifactPrivate:
		return 1
	case extensions.ArtifactSecret:
		return 2
	default:
		return -1
	}
}
