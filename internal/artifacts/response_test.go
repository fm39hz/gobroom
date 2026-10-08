package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestResponseArtifactScopeStreamsAuthorizesAndReleasesLease(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRef := extensions.Ref{Kind: "operation", ID: "image.generate", ContractVersion: 1}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "image.output", ContractVersion: 1}
	if err := catalog.Register(extensions.Descriptor{Ref: operationRef, ImplementationVersion: "1", DisplayName: "Image generation", Description: "Response artifact recipient."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"provider"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"image/png"}, RecipientContracts: []extensions.Ref{operationRef},
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Generated image", Description: "Bounded image output.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	contracts, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 4096, MaxBodyBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "provider", IssuerRef: &extensions.Ref{Kind: "provider-definition", ID: "image-host", ContractVersion: 1}, ProviderDefinitionID: "image-host", ConnectionID: "conn-a", ModelIdentity: "image-v1"}
	receiver := extensions.ArtifactOwner{Domain: "harness", ClientContract: "openai_chat"}
	outputs := []extensions.ArtifactOutput{{Role: "image", TypeRef: artifactRef, MinCount: 1, MaxCount: 1}}
	scope, err := NewResponseScope(store, contracts, owner, receiver, operationRef, outputs, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithResponseScope(context.Background(), scope)
	want := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	artifact, err := PutResponseArtifactFromContext(ctx, artifactRef, "image", "image/png", extensions.ArtifactPrivate, bytes.NewReader(want), 512, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldBody, err := store.Put(context.Background(), owner, "image/png", bytes.NewReader([]byte("old")), 512, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	oldArtifact := artifact.Clone()
	oldArtifact.Body = &oldBody
	oldArtifact.SizeBytes, oldArtifact.SHA256, oldArtifact.ExpiresAt = oldBody.SizeBytes, oldBody.SHA256, oldBody.ExpiresAt
	if _, err := OpenArtifactFromContext(WithAccess(ctx, scope.Access()), oldArtifact); err == nil {
		t.Fatal("renderer response scope opened an artifact from an earlier request")
	}
	tamperedArtifact := artifact.Clone()
	tamperedArtifact.Role = "other"
	if _, err := OpenArtifactFromContext(WithAccess(ctx, scope.Access()), tamperedArtifact); err == nil {
		t.Fatal("renderer response scope accepted a rewritten artifact reference")
	}
	if err := store.Release(owner, oldBody); err != nil {
		t.Fatal(err)
	}
	lease, err := OpenArtifactFromContext(WithAccess(ctx, scope.Access()), artifact)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("artifact bytes=%v want=%v", got, want)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenArtifactFromContext(WithAccess(ctx, scope.Access()), artifact); err == nil {
		t.Fatal("response artifact remained readable after its scope closed")
	}
}

func TestResponseArtifactScopeRejectsUnauthorizedRecipientWithoutSpooling(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRef := extensions.Ref{Kind: "operation", ID: "image.generate", ContractVersion: 1}
	otherOperation := extensions.Ref{Kind: "operation", ID: "image.inspect", ContractVersion: 1}
	for _, ref := range []extensions.Ref{operationRef, otherOperation} {
		if err := catalog.Register(extensions.Descriptor{Ref: ref, ImplementationVersion: "1", DisplayName: ref.ID, Description: "Fixture operation."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "image.output", ContractVersion: 1}
	policy := extensions.ArtifactPolicy{OwnerDomains: []string{"provider"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest}, SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"image/png"}, RecipientContracts: []extensions.Ref{operationRef}}
	if err := catalog.Register(extensions.Descriptor{Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Image", Description: "Fixture artifact.", ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 32}}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	contracts, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 1024, MaxBodyBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "provider", IssuerRef: &extensions.Ref{Kind: "provider-definition", ID: "image-host", ContractVersion: 1}, ProviderDefinitionID: "image-host"}
	outputs := []extensions.ArtifactOutput{{Role: "image", TypeRef: artifactRef, MaxCount: 1}}
	scope, err := NewResponseScope(store, contracts, owner, extensions.ArtifactOwner{Domain: "harness"}, otherOperation, outputs, 32)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PutResponseArtifactFromContext(WithResponseScope(context.Background(), scope), artifactRef, "image", "image/png", extensions.ArtifactPrivate, bytes.NewReader([]byte("pixels")), 32, 0)
	if err == nil {
		t.Fatal("artifact was spooled for a recipient not authorized by its type contract")
	}
	store.mu.Lock()
	used := store.used
	store.mu.Unlock()
	if used != 0 {
		t.Fatalf("unauthorized artifact retained %d spool bytes", used)
	}
}

func TestResponseArtifactScopeEnforcesAggregateOperationOutputBound(t *testing.T) {
	catalog := extensions.NewCatalog()
	operationRef := extensions.Ref{Kind: "operation", ID: "image.generate", ContractVersion: 1}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "image.output", ContractVersion: 1}
	if err := catalog.Register(extensions.Descriptor{Ref: operationRef, ImplementationVersion: "1", DisplayName: "Image generation", Description: "Response artifact recipient."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	policy := extensions.ArtifactPolicy{OwnerDomains: []string{"provider"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest}, SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"image/png"}, RecipientContracts: []extensions.Ref{operationRef}}
	if err := catalog.Register(extensions.Descriptor{Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Generated image", Description: "Bounded image output.", ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024}}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	contracts, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 4096, MaxBodyBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "provider", IssuerRef: &extensions.Ref{Kind: "provider-definition", ID: "image-host", ContractVersion: 1}, ProviderDefinitionID: "image-host"}
	outputs := []extensions.ArtifactOutput{{Role: "image", TypeRef: artifactRef, MaxCount: 2}}
	scope, err := NewResponseScope(store, contracts, owner, extensions.ArtifactOwner{Domain: "harness"}, operationRef, outputs, 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithResponseScope(context.Background(), scope)
	if _, err := PutResponseArtifactFromContext(ctx, artifactRef, "image", "image/png", extensions.ArtifactPrivate, bytes.NewReader([]byte("123456")), 1024, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := PutResponseArtifactFromContext(ctx, artifactRef, "image", "image/png", extensions.ArtifactPrivate, bytes.NewReader([]byte("abcdef")), 1024, 0); err == nil {
		t.Fatal("aggregate output exceeded the operation byte limit")
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	used := store.used
	store.mu.Unlock()
	if used != 0 {
		t.Fatalf("closed response scope retained %d spool bytes", used)
	}
}
