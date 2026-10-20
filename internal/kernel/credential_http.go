package kernel

import (
	"fmt"
	"net/http"
)

// GuardHTTPClient preserves a caller's client settings without mutating shared
// state. Sources keep standard HTTP/pagination logic, while every RoundTrip is
// fenced. Redirects are not an authorization to forward credential material to
// another action or issuer.
func (l *CredentialLease) GuardHTTPClient(scope CredentialLeaseScope, client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	guarded := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	guarded.Transport = credentialLeaseRoundTripper{lease: l, scope: scope, transport: transport}
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &guarded
}

type credentialLeaseRoundTripper struct {
	lease     *CredentialLease
	scope     CredentialLeaseScope
	transport http.RoundTripper
}

func (t credentialLeaseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("credential lease HTTP request is missing")
	}
	if err := t.lease.ValidateForDispatch(request.Context(), t.scope); err != nil {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, err
	}
	return t.transport.RoundTrip(request)
}
