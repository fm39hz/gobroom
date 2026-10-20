package discovery

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func (s Service) acquireCredentialLease(ctx context.Context, route kernel.Route, scope kernel.CredentialLeaseScope) (*kernel.CredentialLease, error) {
	if s.AcquireCredential == nil {
		return nil, fmt.Errorf("credential lease resolver is unavailable")
	}
	lease, err := s.AcquireCredential(ctx, route, scope)
	if err != nil {
		lease.Release()
		return nil, err
	}
	if lease == nil {
		return nil, fmt.Errorf("credential lease resolver returned no lease")
	}
	return lease, nil
}

func validateCredentialBinding(node store.ProviderNode, selected store.ConnectionRecord, binding store.CredentialBinding) error {
	if binding.ID != selected.ID || binding.Type != selected.CredentialType || !binding.Owner.Matches(node.ID, node.DefinitionRef()) || binding.Revision == "" || binding.PrincipalGeneration == "" {
		return fmt.Errorf("connection auth binding changed; retry the connection operation")
	}
	return nil
}

func discoveryRoute(node store.ProviderNode, selected store.ConnectionRecord, binding store.CredentialBinding, runtime provider.RuntimeBinding, model string, operation bool) kernel.Route {
	route := kernel.Route{ID: selected.ID + ":discovery:" + runtime.ModelSourceRef.ID, NodeID: node.ID, DefinitionID: node.DefinitionID, DefinitionRef: node.DefinitionRef(), DisplayPrefix: node.Prefix, ExternalModel: model, Protocol: runtime.Protocol, BaseURL: node.BaseURL, CredentialID: selected.ID, CredentialType: binding.Type, AuthFlowID: runtime.AuthModes[binding.Type].FlowID, CredentialRecovery: runtime.AuthModes[binding.Type].Recovery, HealthGeneration: kernel.RouteHealthGeneration{Connection: binding.OperationalGeneration(), Credential: binding.Revision}, Enabled: true}
	if operation {
		route.SessionOperationRef = runtime.TaskRef
	}
	return route
}

func discoveryHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}
