package provider

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestUsageDescriptorRegistersMetadataWithoutFactory(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	schema := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.search.units.value", ContractVersion: 1}
	if err := runtime.RegisterSchemaDocument(schema, json.RawMessage(`{"type":"integer","minimum":0}`)); err != nil {
		t.Fatal(err)
	}
	ref := extensions.Ref{Kind: extensions.UsageKind, ID: "fixture.search.units", ContractVersion: 1}
	descriptor := extensions.Descriptor{Ref: ref, ImplementationVersion: "1", DisplayName: "Search units", Description: "Units reported by the provider.", InputSchemaRef: &schema, UsagePolicy: &extensions.UsagePolicy{Unit: "search", MaxValueBytes: 32}}
	if err := runtime.RegisterExtension(descriptor, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateUsageValue(extensions.UsageValue{TypeRef: ref, Value: json.RawMessage(`0`)}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterExtension(descriptor, nil); err == nil {
		t.Fatal("registration mutated a frozen runtime")
	}
	other, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Ref.Kind = "operation"
	if err := other.RegisterExtension(descriptor, nil); err == nil {
		t.Fatal("usage registration accepted an operation descriptor")
	}
}
