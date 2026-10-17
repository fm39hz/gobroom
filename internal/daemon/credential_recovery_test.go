package daemon

import (
	"context"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestCredentialRecoveryDisabledDoesNotResolveOrRefresh(t *testing.T) {
	for _, policy := range []kernel.CredentialRecovery{"", kernel.CredentialRecoveryNone, "unknown"} {
		t.Run(string(policy), func(t *testing.T) {
			flow := &ownerGateCredentialFlow{}
			d, id := cancellationAuthDaemon(t, flow)
			route := credentialRefreshRoute(t, d, id)
			route.CredentialRecovery = policy
			if _, err := d.refreshCredential(context.Background(), route, kernel.Credential{}); err == nil {
				t.Fatal("disabled recovery accepted")
			}
			if flow.resolveCalls.Load() != 0 || flow.refreshCalls.Load() != 0 {
				t.Fatal("disabled recovery reached auth driver")
			}
		})
	}
}
