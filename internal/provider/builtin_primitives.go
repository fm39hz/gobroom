package provider

// NewBuiltinPrimitiveRegistry returns the declarative primitive vocabulary used
// by the first provider plugins. Runtime implementations are registered by
// the adapter layer; this package only defines stable IDs and composition.
func NewBuiltinPrimitiveRegistry() (*PrimitiveRegistry, error) {
	r := NewPrimitiveRegistry()
	primitives := []struct {
		kind PrimitiveKind
		ids  []string
	}{
		{PrimitiveEndpoint, []string{"http-json"}},
		{PrimitiveAuth, []string{"static-secret"}},
		{PrimitiveRequestCodec, []string{"openai-chat-json", "openai-responses-json", "anthropic-messages-json"}},
		{PrimitiveResponseCodec, []string{"openai-sse", "openai-responses-sse", "anthropic-sse", "json"}},
		{PrimitiveModelSource, []string{"openai-models", "static-models"}},
		{PrimitiveErrorClassifier, []string{"http-json"}},
	}
	for _, group := range primitives {
		for _, id := range group.ids {
			if err := r.RegisterPrimitive(group.kind, id); err != nil {
				return nil, err
			}
		}
	}
	definitions := []ProviderDefinition{
		{
			ID: "openai-compatible-chat", Version: "1", DisplayName: "OpenAI-compatible Chat",
			Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
			Operations: map[Operation]OperationBinding{
				OperationChat:   {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, RuntimeAdapterID: "openai-chat", RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseCodec: PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "openai-sse"}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"}},
				OperationModels: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, ModelSource: PrimitiveRef{Kind: PrimitiveModelSource, ID: "openai-models"}},
			},
			Capabilities: CapabilitySet{Chat: true, Streaming: true, Tools: true},
		},
		{
			ID: "openai-compatible-responses", Version: "1", DisplayName: "OpenAI-compatible Responses",
			Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
			Operations: map[Operation]OperationBinding{
				OperationResponses: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, RuntimeAdapterID: "openai-responses", RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-responses-json"}, ResponseCodec: PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "openai-responses-sse"}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"}},
			},
			Capabilities: CapabilitySet{Responses: true, Streaming: true, Tools: true},
		},
		{
			ID: "anthropic-messages", Version: "1", DisplayName: "Anthropic Messages",
			Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret"},
			Operations: map[Operation]OperationBinding{
				OperationMessages: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, RuntimeAdapterID: "anthropic-messages", RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "anthropic-messages-json"}, ResponseCodec: PrimitiveRef{Kind: PrimitiveResponseCodec, ID: "anthropic-sse"}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json"}},
			},
			Capabilities: CapabilitySet{Messages: true, Streaming: true, Tools: true, Thinking: true},
		},
	}
	for _, definition := range definitions {
		if err := r.RegisterDefinition(definition); err != nil {
			return nil, err
		}
	}
	return r, nil
}
