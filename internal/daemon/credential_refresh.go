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
	if route.CredentialType != "" && credential.Type != route.CredentialType {
		return kernel.Credential{}, fmt.Errorf("connection credential mode changed; reload the route")
	}
	flow, ok := d.authFlows[route.AuthFlowID]
	if !ok {
		return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", route.AuthFlowID)
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
	if route.CredentialType != "" && stored.Type != route.CredentialType {
		return kernel.Credential{}, fmt.Errorf("connection credential mode changed; reload the route")
	}
	flow, ok := d.authFlows[route.AuthFlowID]
	if !ok {
		return kernel.Credential{}, fmt.Errorf("auth flow %q is unavailable", route.AuthFlowID)
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
	if err := d.store.CompareAndSwapConnectionCredential(ctx, stored, state.Encoded()); err != nil {
		return kernel.Credential{}, err
	}
	committed, ok := d.store.ConnectionCredentialByID(stored.ID)
	if !ok || committed.Secret != state.Encoded() {
		// A newer manual update won after our commit. Never label stale runtime
		// authorization with that newer revision.
		return d.resolveCredential(ctx, route)
	}
	refreshed.Generation = committed.Revision
	if err := d.resetConnectionRoutingState(route.CredentialID); err != nil {
		return kernel.Credential{}, fmt.Errorf("credential refreshed but health reset failed: %w", err)
	}
	resetConnectionHealth(d.policy.Health, d.kernel.Snapshots.Load().Routes, route.CredentialID, route.NodeID)
	if err := d.server.Reload(); err != nil {
		return kernel.Credential{}, fmt.Errorf("credential refreshed but route generation reload failed: %w", err)
	}
	return refreshed, nil
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
