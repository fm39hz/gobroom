package extensions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageValueBindsExactSchemaAndOwnsMetadata(t *testing.T) {
	catalog := NewCatalog()
	usageV1 := ref(UsageKind, "vendor.example.temperature", 1)
	usageV2 := ref(UsageKind, "vendor.example.temperature", 2)
	schemaV1 := ref(SchemaKind, "vendor.example.temperature.value", 1)
	schemaV2 := ref(SchemaKind, "vendor.example.temperature.value", 2)
	testSchema(t, catalog, schemaV1, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"object","properties":{"celsius":{"type":"number"}},"required":["celsius"],"additionalProperties":false}`)
	testSchema(t, catalog, schemaV2, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"object","properties":{"celsius":{"type":"number"},"quality":{"enum":["measured","estimated"]}},"required":["celsius","quality"],"additionalProperties":false}`)

	policy := &UsagePolicy{
		Unit: "degC", Disclosure: UsageDisclosure("private"), MaxValueBytes: 128,
	}
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: usageV1, ImplementationVersion: "1.0", DisplayName: "Room temperature", Description: "Current measured temperature.",
		InputSchemaRef: &schemaV1, UsagePolicy: policy,
	}); err != nil {
		t.Fatal(err)
	}
	// Version 2 is registered to prove schema binding is exact by version.
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: usageV2, ImplementationVersion: "2.0", DisplayName: "Room temperature v2", Description: "Temperature with quality.",
		InputSchemaRef: &schemaV2, UsagePolicy: &UsagePolicy{Unit: "degC", Disclosure: UsageDisclosure("private"), MaxValueBytes: 128},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}

	valid := UsageValue{TypeRef: usageV1, Value: json.RawMessage(`{"celsius":21.5}`)}
	if err := snapshot.ValidateUsageValue(valid); err != nil {
		t.Fatalf("valid registered usage value rejected: %v", err)
	}
	wrongVersion := valid
	wrongVersion.TypeRef = ref(UsageKind, usageV1.ID, 0)
	if err := snapshot.ValidateUsageValue(wrongVersion); err == nil {
		t.Fatal("accepted usage value with missing contract version")
	}
	wrongVersion.TypeRef.ContractVersion = 3
	if err := snapshot.ValidateUsageValue(wrongVersion); err == nil {
		t.Fatal("accepted unregistered usage contract version")
	}
	wrongKind := valid
	wrongKind.TypeRef = ref("provider", usageV1.ID, 1)
	if err := snapshot.ValidateUsageValue(wrongKind); err == nil {
		t.Fatal("accepted usage value with a non-usage kind")
	}
	wrongSchema := valid
	wrongSchema.Value = json.RawMessage(`{"celsius":21.5,"quality":"measured"}`)
	if err := snapshot.ValidateUsageValue(wrongSchema); err == nil {
		t.Fatal("value valid only under another version's schema was accepted")
	}
	if err := snapshot.ValidateUsageValue(UsageValue{TypeRef: usageV1, Value: json.RawMessage(`{"celsius":`)}); err == nil {
		t.Fatal("malformed JSON usage payload was accepted")
	}

	before := snapshot.Fingerprint()
	policy.Unit = "changed"
	policy.Disclosure = UsageDisclosure("management-visible")
	policy.MaxValueBytes = 1
	if snapshot.Fingerprint() != before {
		t.Fatal("caller mutation changed the frozen catalog fingerprint")
	}
	descriptor, ok := snapshot.UsageDescriptor(usageV1)
	if !ok {
		t.Fatal("exact usage descriptor is not queryable")
	}
	if descriptor.Ref != usageV1 || descriptor.Label != "Room temperature" || descriptor.Description != "Current measured temperature." || descriptor.Unit != "degC" || descriptor.Classification != "private" {
		t.Fatalf("frozen usage metadata changed after caller mutation: %#v", descriptor)
	}
	if descriptor.SchemaRef != schemaV1 || descriptor.SchemaDigest == "" {
		t.Fatalf("usage descriptor is not bound to the exact schema identity and digest: %#v", descriptor)
	}
}

func TestUsageDescriptorRejectsInvalidRegistrationAndEnforcesPayloadBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  *UsagePolicy
		schema  *Ref
		wantErr bool
	}{
		{name: "missing policy", schema: ptrRef(ref(SchemaKind, "vendor.example.value", 1)), wantErr: true},
		{name: "zero byte bound", policy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("private"), MaxValueBytes: 0}, schema: ptrRef(ref(SchemaKind, "vendor.example.value", 1)), wantErr: true},
		{name: "negative byte bound", policy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("private"), MaxValueBytes: -1}, schema: ptrRef(ref(SchemaKind, "vendor.example.value", 1)), wantErr: true},
		{name: "invalid disclosure", policy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("public"), MaxValueBytes: 64}, schema: ptrRef(ref(SchemaKind, "vendor.example.value", 1)), wantErr: true},
		{name: "missing schema", policy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("private"), MaxValueBytes: 64}, schema: ptrRef(ref(SchemaKind, "vendor.example.missing", 1)), wantErr: true},
		{name: "wrong kind schema", policy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("private"), MaxValueBytes: 64}, schema: ptrRef(ref("provider", "vendor.example.value", 1)), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := NewCatalog()
			validSchema := ref(SchemaKind, "vendor.example.value", 1)
			testSchema(t, catalog, validSchema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"string"}`)
			if err := catalog.RegisterDescriptor(Descriptor{
				Ref: ref(UsageKind, "vendor.example.counter", 1), ImplementationVersion: "1", DisplayName: "Counter", Description: "A counter.",
				InputSchemaRef: tc.schema, UsagePolicy: tc.policy,
			}); err != nil {
				if !tc.wantErr {
					t.Fatal(err)
				}
				return
			}
			_, err := catalog.Freeze()
			if tc.wantErr && err == nil {
				t.Fatal("invalid usage descriptor was accepted")
			}
		})
	}

	catalog := NewCatalog()
	schema := ref(SchemaKind, "vendor.example.bounded-value", 1)
	testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"string"}`)
	typeRef := ref(UsageKind, "vendor.example.bounded", 1)
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: typeRef, ImplementationVersion: "1", DisplayName: "Bounded", Description: "Bounded value.",
		InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("private"), MaxValueBytes: 9},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateUsageValue(UsageValue{TypeRef: typeRef, Value: json.RawMessage(`"1234567"`)}); err != nil {
		t.Fatalf("payload at the declared byte limit rejected: %v", err)
	}
	if err := snapshot.ValidateUsageValue(UsageValue{TypeRef: typeRef, Value: json.RawMessage(`"12345678"`)}); err == nil {
		t.Fatal("payload over the declared byte limit was accepted")
	}
}

func TestUsageDescriptorFingerprintAndDependencyLockPinSchema(t *testing.T) {
	makeSnapshot := func(schemaDocument string) (*Snapshot, Ref) {
		t.Helper()
		catalog := NewCatalog()
		schema := ref(SchemaKind, "vendor.example.usage.value", 1)
		testSchema(t, catalog, schema, schemaDocument)
		typeRef := ref(UsageKind, "vendor.example.usage", 1)
		if err := catalog.RegisterDescriptor(Descriptor{
			Ref: typeRef, ImplementationVersion: "1", DisplayName: "Usage", Description: "Registered usage.",
			InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{Unit: "items", Disclosure: UsageDisclosure("private"), MaxValueBytes: 64},
		}); err != nil {
			t.Fatal(err)
		}
		snapshot, err := catalog.Freeze()
		if err != nil {
			t.Fatal(err)
		}
		return snapshot, typeRef
	}
	first, typeRef := makeSnapshot(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"integer"}`)
	second, _ := makeSnapshot(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"number"}`)
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("catalog fingerprint did not include the usage schema digest")
	}
	lock, err := first.LockDependencies([]Ref{typeRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Schemas) != 1 || lock.Schemas[0].Ref != ref(SchemaKind, "vendor.example.usage.value", 1) || lock.Schemas[0].SHA256 == "" {
		t.Fatalf("usage dependency lock omitted its value schema pin: %#v", lock.Schemas)
	}
	if err := first.ValidateDependencyLock(lock); err != nil {
		t.Fatalf("valid usage schema dependency pin rejected: %v", err)
	}
	if err := second.ValidateDependencyLock(lock); err == nil {
		t.Fatal("dependency lock accepted a changed usage schema digest")
	}
}

func TestUsageDescriptorLiteralRefIsData(t *testing.T) {
	catalog := NewCatalog()
	schema := ref(SchemaKind, "vendor.example.usage.literal-ref", 1)
	testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","const":{"$ref":"https://literal.invalid/data"},"examples":[{"$ref":"https://literal.invalid/example"}]}`)
	typeRef := ref(UsageKind, "vendor.example.usage.literal-ref", 1)
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: typeRef, ImplementationVersion: "1", DisplayName: "Literal reference", Description: "A literal object containing a ref-shaped key.",
		InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{MaxValueBytes: 128},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatalf("literal ref-shaped data prevented schema compilation: %v", err)
	}
	value := UsageValue{TypeRef: typeRef, Value: json.RawMessage(`{"$ref":"https://literal.invalid/data"}`)}
	if err := snapshot.ValidateUsageValue(value); err != nil {
		t.Fatalf("literal ref-shaped value was not validated as data: %v", err)
	}
	lock, err := snapshot.LockDependencies([]Ref{typeRef})
	if err != nil {
		t.Fatalf("literal ref-shaped data broke dependency locking: %v", err)
	}
	if len(lock.Schemas) != 1 || lock.Schemas[0].Ref != schema {
		t.Fatalf("dependency lock treated literal annotation/const data as a schema dependency: %#v", lock.Schemas)
	}
}

func TestUsageDescriptorSchemaRefPinsReferencedSchema(t *testing.T) {
	catalog := NewCatalog()
	rootA := ref(SchemaKind, "vendor.example.usage.root-a", 1)
	rootB := ref(SchemaKind, "vendor.example.usage.root-b", 1)
	rootBURI, err := rootB.URI()
	if err != nil {
		t.Fatal(err)
	}
	testSchema(t, catalog, rootB, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"integer","minimum":0}`)
	testSchema(t, catalog, rootA, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","$ref":"`+rootBURI+`"}`)
	typeRef := ref(UsageKind, "vendor.example.usage.root-a", 1)
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: typeRef, ImplementationVersion: "1", DisplayName: "Referenced usage", Description: "A usage schema referring to a registered schema.",
		InputSchemaRef: &rootA, UsagePolicy: &UsagePolicy{MaxValueBytes: 16},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := snapshot.LockDependencies([]Ref{typeRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Schemas) != 2 || lock.Schemas[0].Ref != rootA || lock.Schemas[1].Ref != rootB {
		t.Fatalf("dependency lock omitted a compiler-resolved schema reference: %#v", lock.Schemas)
	}
}

func TestUsageDescriptorDoesNotRequireOrInvokeFactory(t *testing.T) {
	catalog := NewCatalog()
	schema := ref(SchemaKind, "vendor.example.usage.no-factory", 1)
	testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"boolean"}`)
	typeRef := ref(UsageKind, "vendor.example.usage.no-factory", 1)
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: typeRef, ImplementationVersion: "1", DisplayName: "Status", Description: "A status value.",
		InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{Disclosure: UsageDisclosure("private"), MaxValueBytes: 16},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateUsageValue(UsageValue{TypeRef: typeRef, Value: json.RawMessage(`true`)}); err != nil {
		t.Fatalf("usage validation needed an executable factory: %v", err)
	}
	if _, ok := snapshot.UsageDescriptor(typeRef); !ok {
		t.Fatal("factory-free usage descriptor is unavailable")
	}
}

func TestUsageRegistrationRejectsFactoryWithoutExecutingIt(t *testing.T) {
	catalog := NewCatalog()
	schema := ref(SchemaKind, "vendor.example.usage.factory-rejected", 1)
	testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"string","enum":["started","stopped"]}`)
	factoryCalls := 0
	err := catalog.Register(Descriptor{
		Ref: ref(UsageKind, "vendor.example.usage.factory-rejected", 1), ImplementationVersion: "1",
		DisplayName: "State", Description: "Categorical state.", InputSchemaRef: &schema,
		UsagePolicy: &UsagePolicy{MaxValueBytes: 32},
	}, func(json.RawMessage) (any, error) {
		factoryCalls++
		return struct{}{}, nil
	})
	if err == nil {
		t.Fatal("usage registration accepted an executable factory")
	}
	if factoryCalls != 0 {
		t.Fatalf("rejected usage factory executed %d times", factoryCalls)
	}
}

func TestUsageDescriptorRejectsOversizedPolicyStringsAndSchemaDocuments(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		catalog := NewCatalog()
		schema := ref(SchemaKind, "vendor.example.usage.large-metadata", 1)
		testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"null"}`)
		err := catalog.RegisterDescriptor(Descriptor{
			Ref: ref(UsageKind, "vendor.example.usage.large-metadata", 1), ImplementationVersion: "1", DisplayName: strings.Repeat("x", 1<<20), Description: "Large.",
			InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{Unit: "count", Disclosure: UsageDisclosure("private"), MaxValueBytes: 16},
		})
		if err == nil {
			t.Fatal("oversized descriptor metadata was accepted")
		}
	})
	t.Run("schema document", func(t *testing.T) {
		catalog := NewCatalog()
		schema := ref(SchemaKind, "vendor.example.usage.large-schema", 1)
		document := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"` + schemaURI(schema) + `","type":"string","description":"` + strings.Repeat("x", 2<<20) + `"}`
		if err := catalog.RegisterSchema(schema, json.RawMessage(document)); err == nil {
			t.Fatal("oversized usage schema document was accepted")
		}
	})
}

func TestUsageValueDefaultAndHostByteBounds(t *testing.T) {
	catalog := NewCatalog()
	schema := ref(SchemaKind, "vendor.example.usage.large-value", 1)
	testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"string"}`)
	typeRef := ref(UsageKind, "vendor.example.usage.large-value", 1)
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: typeRef, ImplementationVersion: "1", DisplayName: "Large value", Description: "A value with an explicit module bound.",
		InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{MaxValueBytes: 128 * 1024},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(strings.Repeat("x", 64*1024))
	if err != nil {
		t.Fatal(err)
	}
	value := UsageValue{TypeRef: typeRef, Value: encoded}
	if err := snapshot.ValidateUsageValue(value); err == nil {
		t.Fatal("default usage validation accepted a payload over 64 KiB")
	}
	validator, ok := any(snapshot).(interface {
		ValidateUsageValueWithin(UsageValue, int64) error
	})
	if !ok {
		t.Fatal("usage snapshot does not expose explicit host-bounded validation")
	}
	if err := validator.ValidateUsageValueWithin(value, 128*1024); err != nil {
		t.Fatalf("explicit larger host limit rejected a descriptor-bounded payload: %v", err)
	}
	if err := validator.ValidateUsageValueWithin(value, 0); err == nil {
		t.Fatal("usage validation accepted a non-positive host byte limit")
	}
	if err := validator.ValidateUsageValueWithin(value, 32); err == nil {
		t.Fatal("usage validation ignored the smaller host byte limit")
	}
}

func TestUsageRegistrationBoundsSerializedDescriptorBeforeExposure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Descriptor)
	}{
		{
			name: "large setup view",
			mutate: func(descriptor *Descriptor) {
				descriptor.SetupView = json.RawMessage(`{"annotation":"` + strings.Repeat("x", 64*1024) + `"}`)
			},
		},
		{
			name: "large reference identity",
			mutate: func(descriptor *Descriptor) {
				descriptor.Ref.ID += strings.Repeat("x", 64*1024)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := NewCatalog()
			schema := ref(SchemaKind, "vendor.example.usage.descriptor-bound", 1)
			testSchema(t, catalog, schema, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"$id-placeholder","type":"null"}`)
			descriptor := Descriptor{
				Ref: ref(UsageKind, "vendor.example.usage.descriptor-bound", 1), ImplementationVersion: "1",
				DisplayName: "Bounded metadata", Description: "A descriptor subject to the total serialized size bound.",
				InputSchemaRef: &schema, UsagePolicy: &UsagePolicy{MaxValueBytes: 32},
			}
			tc.mutate(&descriptor)
			if err := catalog.RegisterDescriptor(descriptor); err == nil {
				t.Fatal("oversized usage descriptor was registered")
			}
			if _, err := catalog.Freeze(); err != nil {
				t.Fatalf("rejected descriptor affected frozen catalog: %v", err)
			}
		})
	}
}
