package daemon

import (
	"context"
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
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
	if latest.Secret != "" && latest.Secret != current.Secret {
		return latest, nil
	}
	refreshed, err := flow.Refresh(ctx, latest)
	if err := d.credentialContextError(ctx); err != nil {
		return kernel.Credential{}, err
	}
	if err != nil {
		return kernel.Credential{}, err
	}
	secret, err := refreshedCredentialSecret(refreshed)
	if err != nil {
		return kernel.Credential{}, err
	}
	if err := d.store.CompareAndSwapConnectionCredential(ctx, stored, secret); err != nil {
		return kernel.Credential{}, err
	}
	if err := d.resetConnectionRoutingState(route.CredentialID); err != nil {
		return kernel.Credential{}, fmt.Errorf("credential refreshed but health reset failed: %w", err)
	}
	resetConnectionHealth(d.policy.Health, d.kernel.Snapshots.Load().Routes, route.CredentialID, route.NodeID)
	if err := d.server.Reload(); err != nil {
		return kernel.Credential{}, fmt.Errorf("credential refreshed but route generation reload failed: %w", err)
	}
	return refreshed, nil
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
