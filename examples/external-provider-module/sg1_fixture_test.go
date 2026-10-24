package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
)

const sg1PromptText = "sg1 stable harness rule"

type sg1PromptTransform struct{}

func (sg1PromptTransform) Definition() provider.TransformDefinition {
	return provider.TransformDefinition{
		Ref:                   provider.Ref{Kind: provider.RequestTransformKind, ID: "fixture.sg1-prompt", ContractVersion: 1},
		ImplementationVersion: "1", Label: "Stable prompt text", Description: "Updates an existing text prompt without changing its provenance or role.",
		OptionsSchemaRef: &provider.Ref{Kind: provider.SchemaKind, ID: "fixture.sg1-prompt.options", ContractVersion: 1},
		Stage:            provider.TransformBeforeRequirements, Effects: []provider.TransformEffect{provider.TransformPrompt},
	}
}

func (sg1PromptTransform) Apply(_ context.Context, request *provider.NormalizedRequest, raw json.RawMessage) error {
	var options struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return err
	}
	if len(request.Prompt.Layers) != 1 || request.Prompt.Layers[0].Role != "system" || len(request.Prompt.Layers[0].Parts) != 0 {
		return errors.New("SG1 fixture requires an existing plain system prompt")
	}
	request.Prompt.Layers[0].Text = options.Text
	return nil
}

type sg1PromptModule struct{}

func (sg1PromptModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "fixture.sg1-prompt", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (sg1PromptModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterRequestTransform(sg1PromptTransform{}, json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","minLength":1,"maxLength":128}},"required":["text"],"additionalProperties":false}`))
}

func newSG1Host(t *testing.T) *portabilityHost {
	t.Helper()
	return newPortabilityHost(t, sg1PromptModule{}, responseLifecycleModule{})
}

type sg1ModelSpec struct{ Name, Protocol, DefinitionID, BaseURL, ExternalModel string }
type sg1Model struct{ NodeID, ConnectionID, RouteID, PhysicalID, ExternalModel string }

func sg1ConfigureModel(t *testing.T, h *portabilityHost, spec sg1ModelSpec) sg1Model {
	t.Helper()
	create := func(method string, params map[string]any) string {
		var row struct{ ID string }
		if err := json.Unmarshal(portabilityOK(t, h.config.IPCPath, method, params), &row); err != nil || row.ID == "" {
			t.Fatalf("%s: id=%q err=%v", method, row.ID, err)
		}
		return row.ID
	}
	node := create("providers.create", map[string]any{"name": spec.Name, "prefix": spec.Name, "baseUrl": spec.BaseURL, "protocol": spec.Protocol, "definitionID": spec.DefinitionID})
	connection := create("connections.create", map[string]any{"providerNodeID": node, "name": "SG1 account", "credentialType": "api_key", "secret": "sg1-fixture-key"})
	m := sg1Model{NodeID: node, ConnectionID: connection, RouteID: spec.Name + "-route", PhysicalID: spec.Name + "-physical", ExternalModel: spec.ExternalModel}
	portabilityOK(t, h.config.IPCPath, "custom_models.upsert", map[string]any{"id": m.RouteID, "providerNodeID": node, "kind": "custom", "externalID": spec.ExternalModel, "displayName": spec.ExternalModel, "connectionIds": []string{connection}, "profile": map[string]any{"tools.function": map[string]any{"state": "native"}}})
	sg1SetLossPolicy(t, h, m, nil)
	return m
}

func sg1SetLossPolicy(t *testing.T, h *portabilityHost, m sg1Model, allow []string) {
	t.Helper()
	portabilityOK(t, h.config.IPCPath, "physical_models.upsert", map[string]any{"name": m.PhysicalID, "identity": map[string]any{"canonicalName": m.ExternalModel}, "sources": []map[string]any{{"routeId": m.RouteID, "fidelity": "exact"}}, "lossPolicy": map[string]any{"allow": allow}, "enabled": true})
}

func sg1PublishRole(t *testing.T, h *portabilityHost, role string, models ...sg1Model) {
	t.Helper()
	members := make([]map[string]any, 0, len(models))
	for _, m := range models {
		members = append(members, map[string]any{"kind": "physical", "id": m.PhysicalID})
	}
	portabilityOK(t, h.config.IPCPath, "combo_models.upsert", map[string]any{"name": role, "members": members, "discoverable": true, "enabled": true})
}

func sg1PutPrompt(t *testing.T, h *portabilityHost, role string) {
	t.Helper()
	options, err := json.Marshal(struct {
		Text string `json:"text"`
	}{sg1PromptText})
	if err != nil {
		t.Fatal(err)
	}
	portabilityOK(t, h.config.IPCPath, "transform_bindings.upsert", map[string]any{"id": "sg1-prompt-" + role, "transformRef": sg1PromptTransform{}.Definition().Ref, "enabled": true, "scope": provider.TransformScope{Kind: provider.TransformScopeModel, ID: role}, "failureMode": provider.TransformFailClosed, "options": json.RawMessage(options)})
}

func sg1PutResponse(t *testing.T, h *portabilityHost, role string, enabled bool) {
	t.Helper()
	portabilityOK(t, h.config.IPCPath, "transform_bindings.upsert", map[string]any{"id": "sg1-response-" + role, "transformRef": responseLifecycleTransform{}.Definition().Ref, "enabled": enabled, "scope": provider.TransformScope{Kind: provider.TransformScopeModel, ID: role}, "failureMode": provider.TransformFailClosed, "options": json.RawMessage(`{"suffix":"-managed","usageAdjustment":0}`)})
}

func sg1Post(t *testing.T, h *portabilityHost, path string, input any, session string) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodPost, "http://"+h.service.Addresses().HTTP+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer client-key-4b7e")
	if session != "" {
		r.Header.Set("X-Session-ID", session)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}
