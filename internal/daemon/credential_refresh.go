package daemon

import (
	"context"
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func (d *Daemon) resolveCredential(ctx context.Context, route kernel.Route) (kernel.Credential, error) {
	ctx, cancel := d.credentialOperationContext(ctx)
	defer cancel()
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	credential, ok := d.store.ConnectionCredentialByID(route.CredentialID)
	if !ok {
		return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
	}
	flow, err := d.credentialFlowForRoute(route, credential)
	if err != nil {
		return kernel.Credential{}, err
	}
	resolved, err := flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: credential.Type, Secret: credential.Secret})
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	resolved.Generation = credential.Revision
	return resolved, err
}

func (d *Daemon) refreshCredential(ctx context.Context, route kernel.Route, current kernel.Credential) (kernel.Credential, error) {
	ctx, cancel := d.credentialOperationContext(ctx)
	defer cancel()
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	if route.CredentialRecovery != kernel.CredentialRecoveryRefresh {
		return kernel.Credential{}, fmt.Errorf("credential recovery is not enabled for this auth mode")
	}
	unlock, err := d.lockCredentialRefresh(ctx, route.CredentialID)
	if err != nil {
		return kernel.Credential{}, err
	}
	defer unlock()
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	stored, ok := d.store.ConnectionCredentialByID(route.CredentialID)
	if !ok {
		return kernel.Credential{}, fmt.Errorf("connection %q is unavailable", route.CredentialID)
	}
	flow, err := d.credentialFlowForRoute(route, stored)
	if err != nil {
		return kernel.Credential{}, err
	}
	latest, err := flow.Resolve(ctx, provider.AuthInput{ConnectionID: route.CredentialID, Type: stored.Type, Secret: stored.Secret})
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	if err != nil {
		return kernel.Credential{}, err
	}
	latest.Generation = stored.Revision
	if current.Generation != "" && latest.Generation != current.Generation {
		return latest, nil
	}
	if latest.Secret != "" && latest.Secret != current.Secret {
		return latest, nil
	}
	state, err := flow.Refresh(ctx, provider.AuthInput{ConnectionID: stored.ID, Type: stored.Type, Secret: stored.Secret}, latest)
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	if err != nil {
		return kernel.Credential{}, err
	}
	if err := state.Validate(); err != nil {
		return kernel.Credential{}, err
	}
	if state.Encoded() == stored.Secret {
		return latest, nil
	}
	refreshed, err := d.resolveReplacementCredential(ctx, flow, stored, state)
	if err != nil {
		return kernel.Credential{}, err
	}
	commit, err := d.store.CompareAndSwapConnectionCredential(ctx, stored, state.Encoded())
	if err != nil {
		return kernel.Credential{}, err
	}
	refreshed.Generation = commit.Revision
	// Durable activation is daemon-owned. Never rebuild configuration or repeat
	// the token exchange on the inference request's latency-critical path.
	d.signalCredentialActivation()
	return refreshed, nil
}

// The route pins the auth binding. Read state and its current owner together;
// an old snapshot must not reinterpret a new owner's state with its old driver.
func (d *Daemon) credentialFlowForRoute(route kernel.Route, credential store.Credential) (provider.AuthFlow, error) {
	if !credential.Owner.Matches(route.NodeID, route.DefinitionRef) || credential.Type != route.CredentialType {
		return nil, fmt.Errorf("connection auth owner or mode changed; reload the route")
	}
	flowID := d.authFlowID(credential.Owner.Definition, credential.Type)
	if flowID == "" || flowID != route.AuthFlowID {
		return nil, fmt.Errorf("route auth binding does not match the connection owner")
	}
	flow, ok := d.authFlows[flowID]
	if !ok || flow == nil {
		return nil, fmt.Errorf("connection auth primitive is unavailable")
	}
	return flow, nil
}

// Auth primitives own durable serialization. Resolve validates the replacement
// before CAS and derives ephemeral headers/context without persisting them.
func (d *Daemon) resolveReplacementCredential(ctx context.Context, flow provider.AuthFlow, expected store.Credential, state provider.CredentialState) (kernel.Credential, error) {
	if err := state.Validate(); err != nil {
		return kernel.Credential{}, err
	}
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	resolved, err := flow.Resolve(ctx, provider.AuthInput{ConnectionID: expected.ID, Type: expected.Type, Secret: state.Encoded()})
	if ctxErr := d.credentialContextError(ctx); ctxErr != nil {
		return kernel.Credential{}, ctxErr
	}
	if err != nil {
		return kernel.Credential{}, fmt.Errorf("replacement credential state could not be resolved")
	}
	if err := resolved.ValidateRuntimeContext(); err != nil {
		return kernel.Credential{}, err
	}
	resolved.ConnectionID, resolved.Type = expected.ID, expected.Type
	return resolved, nil
}

func (d *Daemon) credentialOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if d.runCtx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(d.runCtx, cancel)
	if d.runCtx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (d *Daemon) credentialContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.runCtx != nil {
		return d.runCtx.Err()
	}
	return nil
}
