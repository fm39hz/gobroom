package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

// Credential resolution and provider refresh may perform network I/O. The
// final dispatch fence reads only live owner/revision metadata, never secrets.
func (d *Daemon) acquireCredentialLease(ctx context.Context, route kernel.Route) (*kernel.CredentialLease, error) {
	material, err := d.resolveCredential(ctx, route)
	if err != nil {
		return nil, err
	}
	if !material.ExpiresAt.IsZero() && !material.ExpiresAt.After(time.Now()) {
		material, err = d.refreshCredential(ctx, route, material)
		if err != nil {
			return nil, err
		}
	}
	return d.issueCredentialLease(ctx, route, material)
}

func (d *Daemon) refreshCredentialLease(ctx context.Context, route kernel.Route, previous *kernel.CredentialLease) (*kernel.CredentialLease, error) {
	material, err := previous.MaterialForRefresh(ctx, kernel.CredentialLeaseScopeForRoute(route))
	if err != nil {
		return nil, err
	}
	material, err = d.refreshCredential(ctx, route, material)
	if err != nil {
		return nil, err
	}
	return d.issueCredentialLease(ctx, route, material)
}

func (d *Daemon) issueCredentialLease(ctx context.Context, route kernel.Route, material kernel.Credential) (*kernel.CredentialLease, error) {
	ctx, cancel := d.credentialOperationContext(ctx)
	defer cancel()
	if err := d.credentialContextError(ctx); err != nil {
		return nil, err
	}
	binding, err := d.store.ConnectionCredentialBinding(ctx, route.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("credential lease binding is unavailable")
	}
	// Resolve may finish after a replacement/rebind. Never attach new authority
	// to material returned for the old durable revision.
	if material.Generation == "" || material.Generation != binding.Revision || material.ConnectionID != binding.ID || material.Type != binding.Type {
		return nil, fmt.Errorf("credential changed during lease acquisition")
	}
	metadata := store.Credential{ID: binding.ID, Type: binding.Type, Revision: binding.Revision, PrincipalGeneration: binding.PrincipalGeneration, Owner: binding.Owner}
	if _, err := d.credentialFlowForRoute(route, metadata); err != nil {
		return nil, err
	}
	expires := time.Now().Add(kernel.MaxCredentialLeaseDuration)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	return kernel.NewCredentialLease(kernel.CredentialLeaseScopeForRoute(route), material, expires, func(dispatchCtx context.Context) error {
		if err := d.credentialContextError(dispatchCtx); err != nil {
			return err
		}
		live, err := d.store.ConnectionCredentialBinding(dispatchCtx, binding.ID)
		if err != nil || live != binding {
			return fmt.Errorf("credential lease binding was revoked or changed")
		}
		return d.credentialContextError(dispatchCtx)
	}, nil)
}
