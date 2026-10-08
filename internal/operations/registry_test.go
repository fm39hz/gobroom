package operations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestChatDefinitionCompilesTypedInputAndPinsReplayContract(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterChatGenerate(catalog, registry); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.Prepare(normalize.Request{
		Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		Messages:     []normalize.Message{{Role: "user", Content: "transcribe then summarize"}},
		Requirements: []normalize.FeatureRequirement{{Ref: extensions.Ref{Kind: "feature-evaluator", ID: "vendor.example.json-output.v1", ContractVersion: 2}, Constraints: json.RawMessage(`{"format":"strict"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Ref != ChatGenerateRef() || prepared.ReplaySafety != ReplayOnRejection || len(prepared.Requirements) != 1 {
		t.Fatalf("prepared operation=%#v", prepared)
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "junior", Operation: normalize.OperationChatGenerate}); err == nil {
		t.Fatal("operation without its registered contract version was accepted")
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 2}); err == nil {
		t.Fatal("unsupported operation contract version was accepted")
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"audio":"not a chat payload"}`)}); err == nil {
		t.Fatal("chat operation accepted an opaque non-chat payload")
	}
}

func TestRegisteredNonChatOperationValidatesItsOwnPayload(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	ref := extensions.Ref{Kind: "operation", ID: "audio.transcribe.v1", ContractVersion: 1}
	if err := registry.RegisterRawPayload(ref, "Transcribe audio", "Return a transcript from audio bytes.", json.RawMessage(`{"type":"object","properties":{"contentType":{"const":"audio/wav"},"data":{"type":"string","minLength":1}},"required":["contentType","data"],"additionalProperties":false}`), ReplayNever); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.Prepare(normalize.Request{Model: "asr", Operation: normalize.Operation("audio.transcribe.v1"), OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"contentType":"audio/wav","data":"AAAA"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Ref != ref || prepared.ReplaySafety != ReplayNever {
		t.Fatalf("prepared audio operation=%#v", prepared)
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "asr", Operation: normalize.Operation("audio.transcribe.v1"), OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"audio":"AAAA"}`)}); err == nil {
		t.Fatal("audio payload that violates the operation schema was accepted")
	}
}

func TestOperationArtifactOutputsAreTypedBoundedAndRecipientScoped(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	registerSchema := func(ref extensions.Ref, schema string) {
		t.Helper()
		document, err := extensions.BindSchemaDocument(ref, json.RawMessage(schema))
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(ref, document); err != nil {
			t.Fatal(err)
		}
	}
	inputRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "test.image-output.input", ContractVersion: 1}
	eventRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "test.image-output.event", ContractVersion: 1}
	registerSchema(inputRef, `{"type":"object"}`)
	registerSchema(eventRef, `{"type":"object","required":["kind"],"properties":{"kind":{"type":"string"}},"additionalProperties":true}`)
	operationRef := extensions.Ref{Kind: "operation", ID: "image.generate", ContractVersion: 1}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "image.generated", ContractVersion: 1}
	policy := extensions.ArtifactPolicy{
		OwnerDomains: []string{"provider"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest},
		SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"image/png"}, RecipientContracts: []extensions.Ref{operationRef},
	}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Generated image", Description: "A response image artifact.",
		ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Definition{
		Ref: operationRef, DisplayName: "Image generation", Description: "Generate image artifact.",
		InputSchemaRef: inputRef, EventSchemaRefs: []extensions.Ref{eventRef},
		ArtifactInputs:  []ArtifactInput{{Role: "image", TypeRef: artifactRef, MinCount: 0, MaxCount: 1}},
		ArtifactOutputs: []ArtifactOutput{{Role: "image", TypeRef: artifactRef, MinCount: 1, MaxCount: 2}},
		ResourceBounds:  DefaultResourceBounds(), ReplaySafety: ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	sealedCatalog, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(sealedCatalog)
	if err != nil {
		t.Fatal(err)
	}
	operationDescriptor, ok := sealedCatalog.Descriptor(operationRef)
	if !ok || len(operationDescriptor.ArtifactOutputs) != 1 || operationDescriptor.ArtifactOutputs[0] != (ArtifactOutput{Role: "image", TypeRef: artifactRef, MinCount: 1, MaxCount: 2}) {
		t.Fatalf("catalog omitted the typed artifact output port: %#v", operationDescriptor.ArtifactOutputs)
	}
	resolved, ok := snapshot.Resolve(operationRef)
	if !ok {
		t.Fatal("registered operation disappeared")
	}
	resolved.ArtifactOutputs[0].MaxCount = 99
	if outputs, ok := snapshot.ArtifactOutputs(operationRef); !ok || outputs[0].MaxCount != 2 {
		t.Fatalf("operation snapshot exposed mutable output ports: %#v", outputs)
	}
	owner := extensions.ArtifactOwner{Domain: "provider", IssuerRef: &extensions.Ref{Kind: "provider-definition", ID: "image-host", ContractVersion: 1}, ProviderDefinitionID: "image-host", ConnectionID: "conn-a", ModelIdentity: "image-v1"}
	receiver := extensions.ArtifactOwner{Domain: "harness", ClientContract: "openai_chat"}
	artifact := extensions.ArtifactRef{
		TypeRef: artifactRef, Role: "image", Owner: owner, ReplayScope: extensions.ArtifactReplayRequest,
		Sensitivity: extensions.ArtifactPrivate, MediaType: "image/png", SizeBytes: 4,
		Body: &extensions.BodyRef{ID: "body-1", MediaType: "image/png", SizeBytes: 4},
	}
	counts := map[string]int{}
	if err := snapshot.ValidateArtifactOutputs(operationRef, []extensions.ArtifactRef{artifact}, receiver, operationRef, counts); err != nil {
		t.Fatalf("valid output transfer rejected: %v", err)
	}
	if err := snapshot.ValidateArtifactOutputCounts(operationRef, counts); err != nil {
		t.Fatalf("required output count rejected: %v", err)
	}
	if err := snapshot.ValidateArtifactOutputs(operationRef, []extensions.ArtifactRef{artifact}, receiver, extensions.Ref{Kind: "operation", ID: "other", ContractVersion: 1}, map[string]int{}); err == nil {
		t.Fatal("output artifact crossed to an undeclared recipient")
	}
	if err := snapshot.ValidateArtifactOutputs(operationRef, []extensions.ArtifactRef{artifact, artifact, artifact}, receiver, operationRef, map[string]int{}); err == nil {
		t.Fatal("output count exceeded its operation port bound")
	}
}

func TestOperationPayloadBoundRejectsOversizedBodyBeforeSchemaDecode(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	ref := extensions.Ref{Kind: "operation", ID: "bounded.payload.v1", ContractVersion: 1}
	if err := registry.RegisterRawPayloadWithBounds(ref, "Bounded", "Bounded request fixture", json.RawMessage(`{"type":"object"}`), ReplayNever, extensions.ResourceBounds{MaxInputBytes: 4, MaxOutputBytes: 16, MaxBufferedBytes: 8, DeadlineMillis: 50}); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, err = snapshot.Prepare(normalize.Request{Model: "test", Operation: normalize.Operation(ref.ID), OperationContractVersion: ref.ContractVersion, OperationPayload: json.RawMessage(`{"x":1}`)})
	if err == nil || !strings.Contains(err.Error(), "4-byte operation limit") {
		t.Fatalf("oversized operation payload error=%v", err)
	}
}

func TestOperationResultSchemaRequiresBoundedProjectorAtRegistration(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	inputRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "result-projector.input", ContractVersion: 1}
	resultRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "result-projector.result", ContractVersion: 1}
	for _, item := range []struct {
		ref   extensions.Ref
		shape json.RawMessage
	}{
		{inputRef, json.RawMessage(`{"type":"object"}`)},
		{resultRef, json.RawMessage(`{"type":"object"}`)},
	} {
		document, err := extensions.BindSchemaDocument(item.ref, item.shape)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(item.ref, document); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Register(Definition{
		Ref:         extensions.Ref{Kind: "operation", ID: "result-projector", ContractVersion: 1},
		DisplayName: "Result projector fixture", Description: "Result schemas require a bounded event projector.",
		InputSchemaRef: inputRef, ResultSchemaRef: &resultRef, ResourceBounds: DefaultResourceBounds(), ReplaySafety: ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err == nil {
		t.Fatal("result schema without runtime projector was registered")
	}
}

func TestOperationAcceptsOnlyRegisteredOwnedArtifacts(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "image.inspect.v1", ContractVersion: 1}
	operationSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "image.inspect.input", ContractVersion: 1}
	operationSchema, err := extensions.BindSchemaDocument(operationSchemaRef, json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(operationSchemaRef, operationSchema); err != nil {
		t.Fatal(err)
	}
	artifactRef := extensions.Ref{Kind: extensions.ArtifactKind, ID: "image.reference", ContractVersion: 1}
	recipientRef := extensions.Ref{Kind: "provider-encoder", ID: "fixture.image-reader", ContractVersion: 1}
	if err := catalog.Register(extensions.Descriptor{Ref: recipientRef, ImplementationVersion: "1", DisplayName: "Image reader", Description: "Artifact transfer test."}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	artifactSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "image.reference.value", ContractVersion: 1}
	artifactSchema, err := extensions.BindSchemaDocument(artifactSchemaRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","format":"uri"}},"required":["url"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterSchema(artifactSchemaRef, artifactSchema); err != nil {
		t.Fatal(err)
	}
	policy := extensions.ArtifactPolicy{OwnerDomains: []string{"harness"}, ReplayScopes: []extensions.ArtifactReplayScope{extensions.ArtifactReplayRequest}, SensitivityLimit: extensions.ArtifactPrivate, AllowedMediaTypes: []string{"application/json"}, RecipientContracts: []extensions.Ref{recipientRef}}
	if err := catalog.Register(extensions.Descriptor{
		Ref: artifactRef, ImplementationVersion: "1", DisplayName: "Image reference", Description: "Image URI input.",
		InputSchemaRef: &artifactSchemaRef, ArtifactPolicy: &policy, ResourceBounds: extensions.ResourceBounds{MaxInputBytes: 1024},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Definition{
		Ref: operationRef, DisplayName: "Inspect image", Description: "Inspect one referenced image.",
		InputSchemaRef: operationSchemaRef, ArtifactInputs: []ArtifactInput{{Role: "image", TypeRef: artifactRef, MinCount: 1, MaxCount: 1}}, ResourceBounds: DefaultResourceBounds(), ReplaySafety: ReplayNever,
		InputPayload:        func(normalize.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		CompileRequirements: func(normalize.Request) ([]normalize.FeatureRequirement, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	operationDescriptor, ok := extensionSnapshot.Descriptor(operationRef)
	if !ok || len(operationDescriptor.ArtifactInputs) != 1 || operationDescriptor.ArtifactInputs[0].Role != "image" || operationDescriptor.ArtifactInputs[0].TypeRef != artifactRef {
		t.Fatalf("operation artifact ports are not visible in the frozen descriptor: %#v", operationDescriptor)
	}
	value := json.RawMessage(`{"url":"https://image.example.test/a.png"}`)
	artifact := extensions.ArtifactRef{TypeRef: artifactRef, Role: "image", Owner: extensions.ArtifactOwner{Domain: "harness"}, ReplayScope: extensions.ArtifactReplayRequest, Sensitivity: extensions.ArtifactPrivate, MediaType: "application/json", SizeBytes: int64(len(value)), Value: value}
	request := normalize.Request{Model: "image-check", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: 1, Artifacts: []extensions.ArtifactRef{artifact}}
	prepared, err := snapshot.Prepare(request)
	if err != nil || len(prepared.Artifacts) != 1 || prepared.Artifacts[0].TypeRef != artifactRef {
		t.Fatalf("owned artifact was not prepared: %#v err=%v", prepared, err)
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "image-check", Operation: normalize.Operation(operationRef.ID), OperationContractVersion: 1}); err == nil {
		t.Fatal("operation accepted a missing required artifact role")
	}
	wrongRole := request
	wrongRole.Artifacts = []extensions.ArtifactRef{artifact}
	wrongRole.Artifacts[0].Role = "unbound"
	if _, err := snapshot.Prepare(wrongRole); err == nil {
		t.Fatal("operation accepted an unbound artifact role")
	}
	duplicate := request
	duplicate.Artifacts = []extensions.ArtifactRef{artifact, artifact}
	if _, err := snapshot.Prepare(duplicate); err == nil {
		t.Fatal("operation accepted more artifacts than the input port allows")
	}
	receiver := extensions.ArtifactOwner{Domain: "provider", ProviderDefinitionID: "image-reader", ConnectionID: "conn"}
	if err := snapshot.ValidateArtifactsForConsumer(operationRef, []extensions.ArtifactRef{artifact}, receiver, recipientRef); err != nil {
		t.Fatalf("declared recipient rejected its operation artifact: %v", err)
	}
	if err := snapshot.ValidateArtifactsForConsumer(operationRef, []extensions.ArtifactRef{artifact}, receiver, extensions.Ref{Kind: "provider-encoder", ID: "unlisted", ContractVersion: 1}); err == nil {
		t.Fatal("operation artifact transferred to an undeclared recipient")
	}
	request.Artifacts[0].Owner.Domain = "untrusted"
	if _, err := snapshot.Prepare(request); err == nil {
		t.Fatal("artifact from an undeclared owner domain was accepted")
	}
	request.Artifacts[0] = artifact
	request.Artifacts[0].TypeRef.ContractVersion = 2
	if _, err := snapshot.Prepare(request); err == nil {
		t.Fatal("operation accepted an unregistered artifact type version")
	}
}

func TestOperationResourceBoundsTravelWithPreparedPayloadAndFailClosed(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	ref := extensions.Ref{Kind: "operation", ID: "artifact.ingest.v1", ContractVersion: 1}
	bounds := extensions.ResourceBounds{MaxInputBytes: 12, MaxOutputBytes: 64, MaxBufferedBytes: 8, DeadlineMillis: 500}
	if err := registry.RegisterRawPayloadWithBounds(ref, "Ingest", "Bounded fixture operation", json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"],"additionalProperties":false}`), ReplayNever, bounds); err != nil {
		t.Fatal(err)
	}
	extensionsSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionsSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.Prepare(normalize.Request{Model: "artifact", Operation: normalize.Operation(ref.ID), OperationContractVersion: ref.ContractVersion, OperationPayload: json.RawMessage(`{"x":"a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ResourceBounds != bounds {
		t.Fatalf("prepared bounds=%#v, want %#v", prepared.ResourceBounds, bounds)
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "artifact", Operation: normalize.Operation(ref.ID), OperationContractVersion: ref.ContractVersion, OperationPayload: json.RawMessage(`{"x":"a-much-longer-payload"}`)}); err == nil {
		t.Fatal("oversized operation payload was accepted")
	}
}
