package extensions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func ref(kind, id string, version uint64) Ref {
	return Ref{Kind: kind, ID: id, ContractVersion: version}
}

func TestArtifactContractEnforcesOwnerScopeSensitivityAndBoundedBodyRef(t *testing.T) {
	catalog := NewCatalog()
	artifactRef := ref(ArtifactKind, "vendor.example.image", 1)
	valueSchema := ref(SchemaKind, "vendor.example.image.value", 1)
	recipient := ref("provider-encoder", "vendor.example.image-reader", 1)
	testSchema(t, catalog, valueSchema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"object","properties":{"url":{"type":"string","format":"uri"}},"required":["url"],"additionalProperties":false}`)
	if err := catalog.Register(Descriptor{Ref: recipient, ImplementationVersion: "1", DisplayName: "Image consumer", Description: "Fixture recipient."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	policy := ArtifactPolicy{
		OwnerDomains: []string{"provider"}, ReplayScopes: []ArtifactReplayScope{ArtifactReplayRequest, ArtifactReplayPortable},
		SensitivityLimit: ArtifactPrivate, AllowedMediaTypes: []string{"application/json", "image/png"}, RecipientContracts: []Ref{recipient},
	}
	if err := catalog.Register(Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Image reference", Description: "Owned image input.",
		InputSchemaRef: &valueSchema, ArtifactPolicy: &policy, ResourceBounds: ResourceBounds{MaxInputBytes: 1024 * 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactOwner{Domain: "provider", IssuerRef: ptrRef(ref("provider", "image-host", 1)), ConnectionID: "conn-a"}
	inline := ArtifactRef{TypeRef: artifactRef, Owner: owner, ReplayScope: ArtifactReplayRequest, Sensitivity: ArtifactPrivate, MediaType: "application/json", SizeBytes: int64(len(`{"url":"https://img.test/a.png"}`)), Value: []byte(`{"url":"https://img.test/a.png"}`)}
	if err := snapshot.ValidateArtifact(inline, owner, recipient, time.Now()); err != nil {
		t.Fatalf("valid owner-scoped artifact rejected: %v", err)
	}
	portable := inline
	portable.ReplayScope = ArtifactReplayPortable
	otherOwner := ArtifactOwner{Domain: "harness"}
	if err := snapshot.ValidateArtifact(portable, otherOwner, recipient, time.Now()); err != nil {
		t.Fatalf("authorized portable artifact rejected: %v", err)
	}
	if err := snapshot.ValidateArtifact(portable, otherOwner, ref("provider-encoder", "unlisted", 1), time.Now()); err == nil {
		t.Fatal("portable artifact crossed into an undeclared recipient")
	}
	secret := portable
	secret.Sensitivity = ArtifactSecret
	if err := snapshot.ValidateArtifact(secret, otherOwner, recipient, time.Now()); err == nil {
		t.Fatal("secret artifact crossed owner boundary")
	}
	large := inline
	large.SizeBytes = 2 * 1024 * 1024
	large.Value = make([]byte, int(large.SizeBytes))
	if err := snapshot.ValidateArtifact(large, owner, recipient, time.Now()); err == nil {
		t.Fatal("oversized inline artifact was accepted")
	}
	leased := ArtifactRef{TypeRef: artifactRef, Owner: owner, ReplayScope: ArtifactReplaySession, Sensitivity: ArtifactPrivate, MediaType: "image/png", SizeBytes: 32, Body: &BodyRef{ID: "lease-1", MediaType: "image/png", SizeBytes: 32, ExpiresAt: time.Now().Add(time.Minute)}}
	if err := snapshot.ValidateArtifact(leased, owner, recipient, time.Now()); err == nil {
		t.Fatal("non-replayable body lease was used in session replay scope")
	}
}

func ptrRef(ref Ref) *Ref { return &ref }

func testSchema(t *testing.T, catalog *Catalog, schemaRef Ref, document string) {
	t.Helper()
	uri, err := schemaRef.URI()
	if err != nil {
		t.Fatal(err)
	}
	document = strings.ReplaceAll(document, "$id-placeholder", uri)
	if err := catalog.RegisterSchema(schemaRef, json.RawMessage(document)); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPinsSchemaAndModuleVersionsAndValidatesBeforeFactory(t *testing.T) {
	catalog := NewCatalog()
	baseSchema := ref(SchemaKind, "gobroom.schema.limit", 1)
	baseURI, err := baseSchema.URI()
	if err != nil {
		t.Fatal(err)
	}
	testSchema(t, catalog, baseSchema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"integer","minimum":1,"maximum":4096}`)

	optionsSchema := ref(SchemaKind, "vendor.example.codec-options", 2)
	options := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"object","properties":{"maxTokens":{"$ref":"` + baseURI + `"},"mode":{"enum":["fast","accurate"]}},"required":["maxTokens"],"additionalProperties":false}`
	testSchema(t, catalog, optionsSchema, options)

	dependency := ref("transform", "vendor.example.prompt-base", 1)
	codec := ref("provider-encoder", "vendor.example.answer-codec", 2)
	factoryCalls := 0
	for _, registration := range []struct {
		ref          Ref
		displayName  string
		dependencies []Ref
		schema       *Ref
	}{
		{ref: dependency, displayName: "Prompt layer"},
		{ref: codec, displayName: "Answer codec", dependencies: []Ref{dependency}, schema: &optionsSchema},
	} {
		item := registration
		if err := catalog.Register(Descriptor{
			Ref: item.ref, ImplementationVersion: "1.4.0", DisplayName: item.displayName,
			Description: "test extension", OptionsSchemaRef: item.schema,
			Dependencies:   item.dependencies,
			ResourceBounds: ResourceBounds{MaxInputBytes: 4096, MaxBufferedBytes: 4096, DeadlineMillis: 50},
		}, func(options json.RawMessage) (any, error) {
			factoryCalls++
			return string(options), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Fingerprint() == "" {
		t.Fatal("frozen extension snapshot has no fingerprint")
	}
	if _, _, err := snapshot.Bind(ref(codec.Kind, codec.ID, 0), json.RawMessage(`{"maxTokens":20}`)); err == nil {
		t.Fatal("missing contract version resolved implicitly")
	}
	if _, _, err := snapshot.Bind(ref(codec.Kind, codec.ID, 1), json.RawMessage(`{"maxTokens":20}`)); err == nil {
		t.Fatal("different contract version resolved implicitly")
	}
	if _, _, err := snapshot.Bind(codec, json.RawMessage(`{"maxTokens":5000}`)); err == nil {
		t.Fatal("out-of-range options reached the extension factory")
	}
	if factoryCalls != 0 {
		t.Fatalf("invalid config invoked a module factory %d times", factoryCalls)
	}
	_, instance, err := snapshot.Bind(codec, json.RawMessage(`{"maxTokens":20,"mode":"fast"}`))
	if err != nil || instance == nil || factoryCalls != 1 {
		t.Fatalf("valid binding instance=%v calls=%d err=%v", instance, factoryCalls, err)
	}
	if _, ok := snapshot.Descriptor(codec); !ok {
		t.Fatal("exact versioned descriptor is not queryable")
	}
}

func TestCatalogRejectsRemoteSchemaReferencesDuringFreeze(t *testing.T) {
	catalog := NewCatalog()
	schemaRef := ref(SchemaKind, "vendor.example.remote-ref", 1)
	document := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","$ref":"https://schemas.example.test/untrusted.json"}`
	testSchema(t, catalog, schemaRef, document)
	moduleRef := ref("provider-encoder", "vendor.example.remote-test", 1)
	if err := catalog.Register(Descriptor{Ref: moduleRef, ImplementationVersion: "1", DisplayName: "Remote ref", Description: "test", OptionsSchemaRef: &schemaRef}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Freeze(); err == nil || !strings.Contains(err.Error(), "remote or untrusted") {
		t.Fatalf("untrusted schema reference was accepted: %v", err)
	}
}

func TestCatalogRequiresCompleteDependencyGraphAndRejectsCycles(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		catalog := NewCatalog()
		owner := ref("transform", "vendor.example.owner", 1)
		if err := catalog.Register(Descriptor{Ref: owner, ImplementationVersion: "1", DisplayName: "Owner", Description: "test", Dependencies: []Ref{ref("feature", "vendor.example.missing", 1)}, ResourceBounds: ResourceBounds{MaxBufferedBytes: 4096, DeadlineMillis: 50}}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.Freeze(); err == nil || !strings.Contains(err.Error(), "missing dependency") {
			t.Fatalf("missing dependency was accepted: %v", err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		catalog := NewCatalog()
		first := ref("transform", "vendor.example.first", 1)
		second := ref("transform", "vendor.example.second", 1)
		for _, item := range []struct{ own, dep Ref }{{first, second}, {second, first}} {
			dependency := item.dep
			if err := catalog.Register(Descriptor{Ref: item.own, ImplementationVersion: "1", DisplayName: item.own.ID, Description: "test", Dependencies: []Ref{dependency}, ResourceBounds: ResourceBounds{MaxBufferedBytes: 4096, DeadlineMillis: 50}}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := catalog.Freeze(); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
			t.Fatalf("dependency cycle was accepted: %v", err)
		}
	})
}

func TestCatalogOptionsWithoutSchemaAreRejectedAndSnapshotIsImmutable(t *testing.T) {
	catalog := NewCatalog()
	module := ref("policy", "vendor.example.fixed-policy", 1)
	if err := catalog.Register(Descriptor{Ref: module, ImplementationVersion: "1", DisplayName: "Fixed policy", Description: "no options"}, func(json.RawMessage) (any, error) { return "bound", nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshot.Bind(module, json.RawMessage(`{"ignored":true}`)); err == nil {
		t.Fatal("options without a declared schema were accepted")
	}
	if err := catalog.Register(Descriptor{Ref: ref("policy", "vendor.example.late", 1), ImplementationVersion: "1", DisplayName: "Late", Description: "test"}, func(json.RawMessage) (any, error) { return "late", nil }); err == nil {
		t.Fatal("frozen catalog accepted a mutation")
	}
}
