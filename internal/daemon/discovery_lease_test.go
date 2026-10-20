package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestDiscoveryCredentialLeaseRenewalCanCommitCatalog(t *testing.T) {
	flow := &expiringCredentialFlow{}
	d, connectionID := cancellationAuthDaemon(t, flow)
	node, err := d.store.ProviderNodeByConnection(connectionID)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer refreshed-secret" {
			t.Error("model source did not receive renewed material")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"renewed-model"}]}`))
	}))
	defer upstream.Close()
	if _, err := d.store.UpdateProviderNode(store.UpdateProviderNodeInput{ID: node.ID, BaseURL: upstream.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	before, err := d.store.ConnectionCredentialBinding(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	binding := provider.RuntimeBinding{
		DefinitionID: node.DefinitionID, DefinitionRef: node.DefinitionRef(), Operation: provider.OperationModels,
		ModelSourceRef: extensions.Ref{Kind: "model_source", ID: "openai-models", ContractVersion: 1},
		ModelSource:    provider.OpenAIModelSource{}, Endpoint: kernel.HTTPJSONEndpoint{}, Transport: kernel.HTTPTransport{},
		AuthModes: map[string]provider.BoundAuthMode{"oauth2": {FlowID: "oauth:oauth2", Flow: flow, Recovery: kernel.CredentialRecoveryRefresh, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}},
	}
	d.providerBindings = map[string]provider.RuntimeBinding{provider.RuntimeBindingKey(node.DefinitionRef(), provider.OperationModels): binding}
	result, err := d.discoveryService(nil).RefreshConnection(context.Background(), node.ID, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := d.store.ConnectionCredentialBinding(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision == after.Revision || before.PrincipalGeneration != after.PrincipalGeneration || flow.refreshCalls.Load() != 1 {
		t.Fatal("model discovery did not renew material within the same principal")
	}
	models, err := d.store.Models()
	if err != nil {
		t.Fatal(err)
	}
	if result.Models != 1 || len(models) != 1 || models[0].ExternalID != "renewed-model" || result.ConnectionGeneration != after.OperationalGeneration() {
		t.Fatal("catalog import used stale pre-renewal metadata")
	}
}
