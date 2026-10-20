package kernel

import (
	"context"
	"time"
)

// An installed issuer is authoritative, even if the caller supplies transient
// material. Standalone kernels explicitly issue bounded local leases instead.
func (k *Kernel) acquireAttemptCredential(ctx context.Context, route Route, material Credential) (*CredentialLease, error) {
	if k.AcquireCredential != nil {
		return k.AcquireCredential(ctx, route)
	}
	expires := time.Now().Add(MaxCredentialLeaseDuration)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	return NewCredentialLease(CredentialLeaseScopeForRoute(route), material, expires, nil, nil)
}
