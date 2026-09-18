package provider

import "testing"

import "github.com/fm39hz/gobroom/internal/kernel"

func testPrimitiveRegistry() *PrimitiveRegistry {
	r := NewPrimitiveRegistry()
	for _, item := range []struct {
		kind PrimitiveKind
		id   string
	}{
		{PrimitiveEndpoint, "http-json"},
		{PrimitiveAuth, "static-secret"},
		{PrimitiveRequestCodec, "openai-chat-json"},
		{PrimitiveResponseCodec, "openai-sse"},
		{PrimitiveModelSource, "openai-models"},
		{PrimitiveErrorClassifier, "http-json"},
	} {
		if err := r.RegisterPrimitive(item.kind, item.id); err != nil {
			panic(err)
		}
	}
	return r
}

func TestPrimitiveRegistryValidatesComposedProvider(t *testing.T) {
	r := testPrimitiveRegistry()
	err := r.RegisterDefinition(ProviderDefinition{
		ID:          "openai-compatible",
		Version:     "1",
		DisplayName: "OpenAI-compatible",
		Auth:        PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
		Operations: map[Operation]OperationBinding{
			OperationChat: {
				Endpoint:        PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"},
				RequestCodec:    PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"},
				ResponseCodec:   PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "openai-sse"},
				ModelSource:     PrimitiveRef{Kind: PrimitiveModelSource, ID: "openai-models"},
				ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Definition("openai-compatible"); !ok {
		t.Fatal("definition was not registered")
	}
}

func TestPrimitiveRegistryRejectsUnknownPrimitiveAndDuplicateDefinition(t *testing.T) {
	r := NewPrimitiveRegistry()
	if err := r.RegisterDefinition(ProviderDefinition{ID: "bad", Version: "1", DisplayName: "Bad", Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "missing"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "missing"}, ResponseCodec: PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "missing"}}}}); err == nil {
		t.Fatal("expected unknown primitive error")
	}
	r = testPrimitiveRegistry()
	definition := ProviderDefinition{ID: "same", Version: "1", DisplayName: "Same", Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseCodec: PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "openai-sse"}}}}
	if err := r.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterDefinition(definition); err == nil {
		t.Fatal("expected duplicate definition error")
	}
}

func TestRuntimeRegistryAcceptsAdaptersWithoutKernelChanges(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterAdapter("openai-chat", nil); err == nil {
		t.Fatal("expected nil adapter error")
	}
}

func TestProviderDefinitionJSONBindingRoundTripsTypedContract(t *testing.T) {
	r := testPrimitiveRegistry()
	definition := ProviderDefinition{ID: "json-provider", Version: "1", DisplayName: "JSON Provider", Operations: map[Operation]OperationBinding{OperationChat: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseCodec: PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "openai-sse"}}}}
	data, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProviderDefinitionJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterDefinition(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRegistryLoadsExternalManifest(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"id":"external-chat","version":"1","displayName":"External Chat","auth":{"kind":"auth","id":"static-secret"},"operations":{"chat":{"endpoint":{"kind":"endpoint","id":"http-json"},"requestCodec":{"kind":"request_codec","id":"openai-chat-json"},"responseCodec":{"kind":"response_codec","id":"openai-sse"},"errorClassifier":{"kind":"error_classifier","id":"http-json"},"runtimeAdapterId":"openai-chat"}}}`)
	if err := runtime.LoadDefinitionJSON(data); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Primitives.Definition("external-chat"); !ok {
		t.Fatal("external definition was not loaded")
	}
}

func TestDecodeQuotaJSONNormalizesGenericShape(t *testing.T) {
	values, err := DecodeQuotaJSON([]byte(`{"usage":{"consumed":3,"total":10,"left":7},"window":"daily"}`), kernel.Route{NodeID: "node", CredentialID: "conn", ExternalModel: "model"}, "")
	if err != nil || len(values) != 1 {
		t.Fatalf("values=%#v err=%v", values, err)
	}
	if values[0].Used != 3 || values[0].Limit == nil || *values[0].Limit != 10 || values[0].Remaining == nil || *values[0].Remaining != 7 {
		t.Fatalf("snapshot=%#v", values[0])
	}
}

func TestRuntimeBindingBuilderBuildsComposedOperation(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build("openai-compatible-chat", OperationChat)
	if err != nil {
		t.Fatal(err)
	}
	if binding.AdapterID != "openai-chat" || binding.Adapter == nil || binding.Auth == nil {
		t.Fatalf("binding=%#v", binding)
	}
	if binding.ErrorClassifier == nil {
		t.Fatalf("error classifier was not resolved: %#v", binding)
	}
	modelBinding, err := NewRuntimeBindingBuilder(registry).Build("openai-compatible-chat", OperationModels)
	if err != nil || modelBinding.ModelSource == nil {
		t.Fatalf("model source was not resolved: %#v err=%v", modelBinding, err)
	}
	all, err := registry.BuildBindings()
	if err != nil || len(all) < 3 {
		t.Fatalf("bindings=%d err=%v", len(all), err)
	}
}
