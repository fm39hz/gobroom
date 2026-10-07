package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestFileStoreStreamsBoundedOwnerScopedLeases(t *testing.T) {
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 16, MaxBodyBytes: 10, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "provider", ProviderDefinitionID: "vision", ConnectionID: "conn-a"}
	ref, err := store.Put(context.Background(), owner, "image/png", strings.NewReader("123456"), 8, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes != 6 || ref.SHA256 == "" || !ref.Replayable {
		t.Fatalf("body ref metadata=%#v", ref)
	}
	if _, err := store.Open(context.Background(), extensions.ArtifactOwner{Domain: "provider", ConnectionID: "conn-b"}, ref); !errors.Is(err, ErrOwner) {
		t.Fatalf("cross-connection body open error=%v", err)
	}
	lease, err := store.Open(context.Background(), owner, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(owner, ref); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(lease)
	if err != nil || string(body) != "123456" {
		t.Fatalf("active lease lost body: %q err=%v", body, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), owner, ref); !errors.Is(err, ErrExpired) {
		t.Fatalf("released lease was reopened: %v", err)
	}
}

func TestContextArtifactAccessRequiresExactRecipientAndPolicy(t *testing.T) {
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 64, MaxBodyBytes: 32, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	typeRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "gobroom.test.image", ContractVersion: 1}
	recipient := extensions.Ref{Kind: "provider-encoder", ID: "gobroom.test.image-reader", ContractVersion: 1}
	catalog := extensions.NewCatalog()
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"client"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"image/png"},
		RecipientContracts: []extensions.Ref{recipient},
	}
	if err := catalog.Register(extensions.Descriptor{Ref: recipient, ImplementationVersion: "1", DisplayName: "Image reader", Description: "test"}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Register(extensions.Descriptor{Ref: typeRef, ImplementationVersion: "1", DisplayName: "Image", Description: "test", ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 32}}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	owner := extensions.ArtifactOwner{Domain: "client", ClientContract: "openai"}
	body, err := store.Put(context.Background(), owner, "image/png", strings.NewReader("pixels"), 32, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	artifact := extensions.ArtifactRef{
		TypeRef: typeRef, Owner: owner, ReplayScope: extensions.ArtifactReplayRequest,
		Sensitivity: extensions.ArtifactPrivate, MediaType: body.MediaType, SizeBytes: body.SizeBytes,
		SHA256: body.SHA256, ExpiresAt: body.ExpiresAt, Body: &body,
	}
	ctx := WithAccess(context.Background(), Access{
		Store: store, Catalog: snapshot,
		Receiver:  extensions.ArtifactOwner{Domain: "provider", ProviderDefinitionID: "test", ConnectionID: "conn"},
		Recipient: recipient,
	})
	lease, err := OpenArtifactFromContext(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(lease)
	if err != nil || string(got) != "pixels" {
		t.Fatalf("authorized artifact read=%q err=%v", got, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}

	wrongRecipient := artifact
	wrongRecipient.TypeRef = typeRef
	wrongCtx := WithAccess(context.Background(), Access{
		Store: store, Catalog: snapshot,
		Receiver:  extensions.ArtifactOwner{Domain: "provider", ProviderDefinitionID: "test", ConnectionID: "conn"},
		Recipient: extensions.Ref{Kind: "provider-encoder", ID: "unlisted", ContractVersion: 1},
	})
	if _, err := OpenArtifactFromContext(wrongCtx, wrongRecipient); err == nil {
		t.Fatal("artifact crossed into an undeclared recipient")
	}
}

func TestFileStoreEnforcesPerBodyTotalAndContextBounds(t *testing.T) {
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 10, MaxBodyBytes: 8, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "harness"}
	first, err := store.Put(context.Background(), owner, "application/octet-stream", strings.NewReader("1234567"), 8, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), owner, "application/octet-stream", strings.NewReader("1234"), 8, time.Minute, true); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("total spool bound was not enforced: %v", err)
	}
	if _, err := store.Put(context.Background(), owner, "application/octet-stream", strings.NewReader("123456789"), 8, time.Minute, true); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("per-body limit was not enforced: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(canceled, owner, "application/octet-stream", strings.NewReader("x"), 8, time.Minute, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled body write continued: %v", err)
	}
	if err := store.Release(owner, first); err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(context.Background(), owner, "application/octet-stream", strings.NewReader("1234"), 8, time.Minute, true)
	if err != nil || second.SizeBytes != 4 {
		t.Fatalf("capacity was not reclaimed after release: ref=%#v err=%v", second, err)
	}
}

func TestFileStorePrunesExpiredBodyReferences(t *testing.T) {
	store, err := NewStore(t.TempDir(), Limits{MaxBytes: 8, MaxBodyBytes: 8, DefaultTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := extensions.ArtifactOwner{Domain: "provider"}
	ref, err := store.Put(context.Background(), owner, "application/octet-stream", strings.NewReader("data"), 8, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries[ref.ID].ref.ExpiresAt = time.Now().Add(-time.Second)
	store.mu.Unlock()
	if removed := store.Prune(time.Now()); removed != 1 {
		t.Fatalf("expired references pruned=%d", removed)
	}
	if _, err := store.Open(context.Background(), owner, ref); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired body was still openable: %v", err)
	}
}
