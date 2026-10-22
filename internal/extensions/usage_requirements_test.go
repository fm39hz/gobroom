package extensions

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func usageRequirementFactory(json.RawMessage) (any, error) { return struct{}{}, nil }

func assertUsageRequirementRejected(t *testing.T, catalog *Catalog, descriptor Descriptor, reason string) {
	t.Helper()
	err := catalog.Register(descriptor, usageRequirementFactory)
	if err == nil {
		_, err = catalog.Freeze()
	}
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("expected usage requirement rejection containing %q, got %v", reason, err)
	}
}

func registerUsageRequirementTarget(t *testing.T, catalog *Catalog, target Ref) {
	t.Helper()
	schemaRef := Ref{Kind: SchemaKind, ID: target.ID + ".value", ContractVersion: target.ContractVersion}
	if err := catalog.RegisterSchema(schemaRef, mustUsageRequirementSchema(t, schemaRef)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterDescriptor(Descriptor{
		Ref: target, ImplementationVersion: "1", DisplayName: "Usage detail", Description: "A registered usage detail.",
		InputSchemaRef: &schemaRef, UsagePolicy: &UsagePolicy{Unit: "items", Disclosure: UsageDisclosure("private"), MaxValueBytes: 64},
	}); err != nil {
		t.Fatal(err)
	}
}

func mustUsageRequirementSchema(t *testing.T, ref Ref) []byte {
	t.Helper()
	document, err := BindSchemaDocument(ref, []byte(`{"type":"integer","minimum":0}`))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func usageRequirementOperation(targets ...Ref) Descriptor {
	return Descriptor{
		Ref:                   Ref{Kind: "operation", ID: "vendor.example.generate", ContractVersion: 1},
		ImplementationVersion: "1", DisplayName: "Generate", Description: "Generate a result.",
		UsageRequirements: UsageRequirements{RequireReported: true, RequiredExtensions: targets},
	}
}

func TestUsageRequirementsRequireExactRegisteredUsageRefs(t *testing.T) {
	tests := []struct {
		name   string
		ref    Ref
		reason string
	}{
		{name: "missing", ref: Ref{Kind: UsageKind, ID: "vendor.example.missing", ContractVersion: 1}, reason: "missing dependency"},
		{name: "wrong kind", ref: Ref{Kind: "provider", ID: "vendor.example.units", ContractVersion: 1}, reason: "required usage extensions"},
		{name: "missing version", ref: Ref{Kind: UsageKind, ID: "vendor.example.units"}, reason: "required usage extensions"},
		{name: "wrong version", ref: Ref{Kind: UsageKind, ID: "vendor.example.units", ContractVersion: 2}, reason: "missing dependency"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := NewCatalog()
			target := Ref{Kind: UsageKind, ID: "vendor.example.units", ContractVersion: 1}
			registerUsageRequirementTarget(t, catalog, target)
			assertUsageRequirementRejected(t, catalog, usageRequirementOperation(tc.ref), tc.reason)
		})
	}
}

func TestUsageRequirementsRejectDuplicateAndMoreThan32Refs(t *testing.T) {
	valid := Ref{Kind: UsageKind, ID: "vendor.example.units", ContractVersion: 1}
	t.Run("duplicate", func(t *testing.T) {
		catalog := NewCatalog()
		registerUsageRequirementTarget(t, catalog, valid)
		assertUsageRequirementRejected(t, catalog, usageRequirementOperation(valid, valid), "duplicate required usage")
	})
	t.Run("32 accepted", func(t *testing.T) {
		catalog := NewCatalog()
		refs := make([]Ref, 32)
		for i := range refs {
			refs[i] = Ref{Kind: UsageKind, ID: "vendor.example.bounded-" + strconv.Itoa(i), ContractVersion: 1}
			registerUsageRequirementTarget(t, catalog, refs[i])
		}
		if err := catalog.Register(usageRequirementOperation(refs...), usageRequirementFactory); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.Freeze(); err != nil {
			t.Fatalf("rejected exactly 32 required usage refs: %v", err)
		}
	})
	t.Run("32 bound", func(t *testing.T) {
		refs := make([]Ref, 33)
		catalog := NewCatalog()
		for i := range refs {
			refs[i] = Ref{Kind: UsageKind, ID: "vendor.example.units-" + strconv.Itoa(i), ContractVersion: 1}
			registerUsageRequirementTarget(t, catalog, refs[i])
		}
		assertUsageRequirementRejected(t, catalog, usageRequirementOperation(refs...), "32-ref limit")
	})
}

func TestUsageRequirementsCloneFingerprintAndDependencyLock(t *testing.T) {
	target := Ref{Kind: UsageKind, ID: "vendor.example.search-units", ContractVersion: 3}
	makeSnapshot := func(requirements UsageRequirements) (*Snapshot, Descriptor) {
		t.Helper()
		catalog := NewCatalog()
		registerUsageRequirementTarget(t, catalog, target)
		operation := usageRequirementOperation(requirements.RequiredExtensions...)
		operation.UsageRequirements.RequireReported = requirements.RequireReported
		if err := catalog.Register(operation, usageRequirementFactory); err != nil {
			t.Fatal(err)
		}
		snapshot, err := catalog.Freeze()
		if err != nil {
			t.Fatal(err)
		}
		return snapshot, operation
	}

	callerRefs := []Ref{target}
	snapshot, _ := makeSnapshot(UsageRequirements{RequireReported: true, RequiredExtensions: callerRefs})
	callerRefs[0] = Ref{Kind: UsageKind, ID: "mutated", ContractVersion: 1}
	before := snapshot.Fingerprint()
	operationRef := Ref{Kind: "operation", ID: "vendor.example.generate", ContractVersion: 1}
	descriptor, ok := snapshot.Descriptor(operationRef)
	if !ok || len(descriptor.UsageRequirements.RequiredExtensions) != 1 || descriptor.UsageRequirements.RequiredExtensions[0] != target {
		t.Fatalf("frozen descriptor did not own exact requirement refs: %#v", descriptor.UsageRequirements)
	}
	descriptor.UsageRequirements.RequiredExtensions[0].ID = "mutated-read"
	if snapshot.Fingerprint() != before {
		t.Fatal("mutating a descriptor read changed the frozen fingerprint")
	}
	lock, err := snapshot.LockDependencies([]Ref{operationRef})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pin := range lock.Extensions {
		if pin.Ref == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("dependency lock omitted required usage extension %s: %#v", target.Key(), lock.Extensions)
	}
	withoutRequirement, _ := makeSnapshot(UsageRequirements{RequireReported: true})
	if snapshot.Fingerprint() == withoutRequirement.Fingerprint() {
		t.Fatal("catalog fingerprint omitted operation usage requirements")
	}
}

func TestUsageRequirementsAreOperationOnlyAndZeroValueIsAllowed(t *testing.T) {
	for _, kind := range []string{"provider-definition", "transform"} {
		catalog := NewCatalog()
		descriptor := usageRequirementOperation()
		descriptor.Ref.Kind = kind
		descriptor.UsageRequirements = UsageRequirements{RequireReported: true}
		descriptor.ResourceBounds = ResourceBounds{MaxBufferedBytes: 1024, DeadlineMillis: 100}
		assertUsageRequirementRejected(t, catalog, descriptor, "usage requirements may be declared only by operations")
	}
	catalog := NewCatalog()
	if err := catalog.Register(Descriptor{
		Ref:                   Ref{Kind: "operation", ID: "vendor.example.normal", ContractVersion: 1},
		ImplementationVersion: "1", DisplayName: "Normal", Description: "No accounting requirement.",
	}, usageRequirementFactory); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Freeze(); err != nil {
		t.Fatalf("zero-value usage requirements rejected: %v", err)
	}
}
