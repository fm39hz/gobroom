package kernel

import (
	"context"
	"fmt"
	"io"
)

// GuardTransport leaves request encoding to the source, but retains dispatch
// authority at every transport call (including pagination or multi-report
// reads). The owner must retain the lease until the source has finished.
func (l *CredentialLease) GuardTransport(scope CredentialLeaseScope, transport Transport) Transport {
	return credentialLeaseTransport{lease: l, scope: scope, transport: transport}
}

type credentialLeaseTransport struct {
	lease     *CredentialLease
	scope     CredentialLeaseScope
	transport Transport
}

func (t credentialLeaseTransport) ID() string {
	if t.transport == nil {
		return "credential-lease"
	}
	return t.transport.ID()
}

func (t credentialLeaseTransport) Execute(ctx context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	err := t.lease.ValidateForDispatch(ctx, t.scope)
	if err == nil && t.transport == nil {
		err = fmt.Errorf("credential lease transport is unavailable")
	}
	if err != nil {
		if body, ok := request.Body.(io.Closer); ok {
			_ = body.Close()
		}
		return UpstreamResponse{}, err
	}
	return t.transport.Execute(ctx, request)
}
