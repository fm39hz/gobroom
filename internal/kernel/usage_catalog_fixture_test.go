package kernel

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

func fixtureUsageCatalog(t *testing.T, schema string, refs ...extensions.Ref) *extensions.Snapshot {
	t.Helper()
	catalog := extensions.NewCatalog()
	schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.accounting.value", ContractVersion: 1}
	document, err := extensions.BindSchemaDocument(schemaRef, json.RawMessage(schema))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(schemaRef, document); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if err := catalog.RegisterDescriptor(extensions.Descriptor{Ref: ref, ImplementationVersion: "1", DisplayName: "Fixture usage", Description: "Registered fixture accounting.", InputSchemaRef: &schemaRef, UsagePolicy: &extensions.UsagePolicy{MaxValueBytes: 65536}}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func fixtureUsageOperation(t *testing.T, requirements extensions.UsageRequirements, bounds extensions.ResourceBounds) (*operations.Snapshot, *extensions.Snapshot, extensions.Ref) {
	t.Helper()
	catalog := extensions.NewCatalog()
	registry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	input := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.usage-required.input", ContractVersion: 1}
	document, err := extensions.BindSchemaDocument(input, json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(input, document); err != nil {
		t.Fatal(err)
	}
	operation := extensions.Ref{Kind: "operation", ID: "fixture.usage-required", ContractVersion: 1}
	if err := registry.Register(operations.Definition{
		Ref: operation, DisplayName: "Required accounting", Description: "Fixture accounting completion.",
		InputSchemaRef: input, ResourceBounds: bounds, ReplaySafety: operations.ReplayOnRejection, UsageRequirements: requirements,
		InputPayload:        func(request normalize.Request) (json.RawMessage, error) { return request.OperationPayload, nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	frozen, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, frozen, operation
}
