package artifacts

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestInlineResponseDisclosurePreservesConnectionReplayScope(t *testing.T) {
	issuer := extensions.Ref{Kind: "provider-definition", ID: "jobs", ContractVersion: 1}
	operation := extensions.Ref{Kind: "operation", ID: "jobs.create", ContractVersion: 1}
	artifactType := extensions.Ref{Kind: extensions.ArtifactKind, ID: "jobs.handle", ContractVersion: 1}
	schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "jobs.handle.value", ContractVersion: 1}
	catalog := extensions.NewCatalog()
	for _, ref := range []extensions.Ref{issuer, operation} {
		if err := catalog.Register(extensions.Descriptor{Ref: ref, ImplementationVersion: "1", DisplayName: "Fixture", Description: "Fixture."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	document, err := extensions.BindSchemaDocument(schemaRef, json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","minLength":1}},"required":["id"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(schemaRef, document); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactType, ImplementationVersion: "1", DisplayName: "Job handle", Description: "A provider handle.", InputSchemaRef: &schemaRef,
		ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 128},
		ArtifactPolicy: &extensions.ArtifactPolicy{OwnerDomains: []string{"provider"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayConnection}, SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"application/json"}, RecipientContracts: []extensions.Ref{issuer, operation}},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 256, MaxBodyBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "provider", IssuerRef: &issuer, ProviderDefinitionID: issuer.ID, ConnectionID: "account-a", ModelIdentity: "generator"}
	client := extensions.ArtifactOwner{Domain: "harness", ClientContract: "jobs-json"}
	scope, err := NewResponseScope(store, snapshot, owner, client, operation, []extensions.ArtifactOutput{{Role: "job", TypeRef: artifactType, MinCount: 1, MaxCount: 1}}, 128)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithResponseScope(context.Background(), scope)
	for _, invalid := range []struct {
		value       []byte
		ttl         time.Duration
		sensitivity extensions.ArtifactSensitivity
	}{
		{[]byte(`{"id":7}`), time.Minute, extensions.ArtifactPrivate},
		{[]byte(`{"id":"job-1"}`), 0, extensions.ArtifactPrivate},
		{[]byte(`{"id":"job-1"}`), time.Minute, extensions.ArtifactSecret},
		{make([]byte, 129), time.Minute, extensions.ArtifactPrivate},
	} {
		if _, err := PublishInlineResponseArtifactFromContext(ctx, artifactType, "job", "application/json", extensions.ArtifactReplayConnection, invalid.sensitivity, invalid.value, invalid.ttl); err == nil {
			t.Fatal("invalid inline publication was accepted")
		}
	}
	value := []byte(`{"id":"job-1"}`)
	artifact, err := PublishInlineResponseArtifactFromContext(ctx, artifactType, "job", "application/json", extensions.ArtifactReplayConnection, extensions.ArtifactPrivate, value, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	value[0] = '['
	if artifact.Body != nil || string(artifact.Value) != `{"id":"job-1"}` || !artifact.Owner.Equal(owner) || artifact.ReplayScope != extensions.ArtifactReplayConnection {
		t.Fatalf("publication changed ownership/scope or retained caller bytes: %#v", artifact)
	}
	if err := snapshot.ValidateArtifactDisclosure(artifact, client, operation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateArtifactDisclosure(artifact, client, extensions.Ref{Kind: "operation", ID: "unlisted", ContractVersion: 1}, time.Now()); err == nil {
		t.Fatal("disclosure ignored the exact output recipient allowlist")
	}
	if err := snapshot.ValidateArtifact(artifact, client, operation, time.Now()); err == nil {
		t.Fatal("delivery gave the harness connection replay authority")
	}
	consumer := owner
	consumer.SessionID = "later-request"
	if err := snapshot.ValidateArtifact(artifact, consumer, issuer, time.Now()); err != nil {
		t.Fatal(err)
	}
	consumer.ConnectionID = "account-b"
	if err := snapshot.ValidateArtifact(artifact, consumer, issuer, time.Now()); err == nil {
		t.Fatal("job handle crossed connection owners")
	}
	consumer = owner
	otherIssuer := issuer
	otherIssuer.ContractVersion++
	consumer.IssuerRef = &otherIssuer
	if err := snapshot.ValidateArtifact(artifact, consumer, issuer, time.Now()); err == nil {
		t.Fatal("job handle crossed exact issuer contracts")
	}
	if err := snapshot.ValidateArtifactDisclosure(artifact, client, operation, artifact.ExpiresAt); err == nil {
		t.Fatal("expired handle was disclosed")
	}
	if _, err := PublishInlineResponseArtifactFromContext(ctx, artifactType, "job", "application/json", extensions.ArtifactReplayConnection, extensions.ArtifactPrivate, []byte(`{"id":"job-2"}`), time.Minute); err == nil {
		t.Fatal("inline output exceeded declared count")
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateArtifact(artifact, owner, issuer, time.Now()); err != nil {
		t.Fatalf("response cleanup invalidated the client-held inline handle: %v", err)
	}
	if _, err := PublishInlineResponseArtifactFromContext(ctx, artifactType, "job", "application/json", extensions.ArtifactReplayConnection, extensions.ArtifactPrivate, []byte(`{"id":"job-3"}`), time.Minute); err == nil {
		t.Fatal("closed scope allowed publication")
	}
}
