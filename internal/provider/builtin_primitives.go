package provider

import (
	"encoding/json"
	"fmt"
	"github.com/fm39hz/gobroom/internal/adapter/usagewire"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

// NewBuiltinPrimitiveRegistry returns the declarative primitive vocabulary used
// by the first provider plugins. Runtime implementations are registered by
// the adapter layer; this package only defines stable IDs and composition.
func NewBuiltinPrimitiveRegistry() (*PrimitiveRegistry, error) {
	r := NewPrimitiveRegistry()
	if err := usagewire.RegisterReportedCharge(r.catalog); err != nil {
		return nil, fmt.Errorf("register reported charge contract: %w", err)
	}
	if err := operations.RegisterChatGenerate(r.catalog, r.Operations); err != nil {
		return nil, fmt.Errorf("register built-in chat operation: %w", err)
	}
	if err := kernel.RegisterBuiltinStrategyExtensions(r.catalog); err != nil {
		return nil, fmt.Errorf("register built-in strategies: %w", err)
	}
	primitives := []struct {
		kind PrimitiveKind
		ids  []string
	}{
		{PrimitiveTransport, []string{"http"}},
		{PrimitiveAuth, []string{"static-secret", "none"}},
		{PrimitiveRequestCodec, []string{"openai-chat-json", "openai-responses-json", "anthropic-messages-json", "gemini-json"}},
		{PrimitiveResponseDecoder, []string{"openai-sse", "openai-responses-sse", "anthropic-sse", "gemini-json"}},
		{PrimitiveModelSource, []string{"openai-models", "anthropic-models"}},
		{PrimitiveSessionStore, []string{"session"}},
	}
	for _, group := range primitives {
		for _, id := range group.ids {
			if err := r.RegisterPrimitive(group.kind, id); err != nil {
				return nil, err
			}
		}
	}
	for _, configured := range []struct {
		kind   PrimitiveKind
		id     string
		ref    extensions.Ref
		schema func() (json.RawMessage, error)
	}{
		{PrimitiveEndpoint, "http-json", endpointOptionsSchemaRef, endpointOptionsSchema},
		{PrimitiveModelSource, "static-models", staticModelSourceSchemaRef, staticModelSourceOptionsSchema},
		{PrimitiveUsageSource, "http-header-usage", usageOptionsSchemaRef, usageOptionsSchema},
		{PrimitiveQuotaSource, "http-json-quota", quotaSourceOptionsSchemaRef, quotaSourceOptionsSchema},
		{PrimitiveErrorClassifier, "http-json", errorOptionsSchemaRef, httpJSONErrorOptionsSchema},
	} {
		document, err := configured.schema()
		if err != nil {
			return nil, fmt.Errorf("build options schema for %s/%s: %w", configured.kind, configured.id, err)
		}
		if err := r.RegisterPrimitiveWithOptionsSchema(configured.kind, configured.id, 1, configured.ref, document); err != nil {
			return nil, err
		}
	}
	definitions := []ProviderDefinition{
		{
			ContractVersion: 1, ID: "gemini", Version: "1", DisplayName: "Google Gemini", DefaultCredentialType: "api_key", AuthModes: []AuthModeDefinition{{CredentialType: "api_key", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "x-goog-api-key"}}},
			Session:    PrimitiveRef{Kind: PrimitiveSessionStore, ID: "session", ContractVersion: 1},
			Operations: map[Operation]OperationBinding{OperationChat: {Protocol: kernel.ProtocolGemini, TaskRef: OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: "gemini", Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "gemini-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "gemini-json", ContractVersion: 1}, UsageSource: PrimitiveRef{Kind: PrimitiveUsageSource, ID: "http-header-usage", ContractVersion: 1}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}}},
		},
		{
			ContractVersion: 1, ID: "openai-compatible-chat", Version: "1", DisplayName: "OpenAI-compatible Chat",
			DefaultCredentialType: "api_key", AuthModes: []AuthModeDefinition{{CredentialType: "api_key", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
			Operations: map[Operation]OperationBinding{
				OperationChat:   {Protocol: kernel.ProtocolOpenAIChat, TaskRef: OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.FormatOpenAIChat, Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1}, UsageSource: PrimitiveRef{Kind: PrimitiveUsageSource, ID: "http-header-usage", ContractVersion: 1}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}},
				OperationModels: {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, ModelSource: PrimitiveRef{Kind: PrimitiveModelSource, ID: "openai-models", ContractVersion: 1}},
			},
			Capabilities: CapabilitySet{Chat: true, Streaming: true, Tools: true},
		},
		{
			ContractVersion: 1, ID: "openai-compatible-responses", Version: "1", DisplayName: "OpenAI-compatible Responses",
			DefaultCredentialType: "api_key", AuthModes: []AuthModeDefinition{{CredentialType: "api_key", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
			Session: PrimitiveRef{Kind: PrimitiveSessionStore, ID: "session", ContractVersion: 1},
			Operations: map[Operation]OperationBinding{
				OperationResponses: {Protocol: kernel.ProtocolOpenAIResponses, TaskRef: OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.FormatOpenAIResponses, Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-responses-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-responses-sse", ContractVersion: 1}, UsageSource: PrimitiveRef{Kind: PrimitiveUsageSource, ID: "http-header-usage", ContractVersion: 1}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1},
					ErrorClassifierOptions: ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{400, 409}, Codes: []string{"previous_response_not_found"}}}, ResponseContinuityResetSignals: []HTTPJSONErrorSignal{{Codes: []string{"previous_response_not_found"}}}}},
				},
			},
			Capabilities: CapabilitySet{Responses: true, Streaming: true, Tools: true},
		},
		{
			ContractVersion: 1, ID: "anthropic-messages", Version: "1", DisplayName: "Anthropic Messages",
			DefaultCredentialType: "api_key", AuthModes: []AuthModeDefinition{{CredentialType: "api_key", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "x-api-key"}}},
			Operations: map[Operation]OperationBinding{
				OperationMessages: {Protocol: kernel.ProtocolAnthropic, TaskRef: OperationRef(normalize.OperationChatGenerate, 1), ProviderFormat: normalize.FormatAnthropic, Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "anthropic-messages-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "anthropic-sse", ContractVersion: 1}, UsageSource: PrimitiveRef{Kind: PrimitiveUsageSource, ID: "http-header-usage", ContractVersion: 1}, ErrorClassifier: PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}},
				OperationModels:   {Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1}, ModelSource: PrimitiveRef{Kind: PrimitiveModelSource, ID: "anthropic-models", ContractVersion: 1}},
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
