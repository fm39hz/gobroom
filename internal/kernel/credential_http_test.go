package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type leaseHTTPRoundTrip func(*http.Request) (*http.Response, error)

func (f leaseHTTPRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCredentialLeaseHTTPGuardChecksEveryPageAndClosesRejectedBody(t *testing.T) {
	var revoked atomic.Bool
	var calls atomic.Int32
	lease, scope, _ := newLeaseFixture(t, func(context.Context) error {
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}, nil)
	defer lease.Release()
	client := lease.GuardHTTPClient(scope, &http.Client{Transport: leaseHTTPRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		revoked.Store(true)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("page")), Header: make(http.Header), Request: r}, nil
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.invalid/models", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	body := &leaseTestBody{Reader: strings.NewReader("second page")}
	request, _ = http.NewRequest(http.MethodGet, "https://upstream.invalid/models?after=first", body)
	if _, err := client.Do(request); err == nil {
		t.Fatal("revoked authority sent another page")
	}
	if calls.Load() != 1 || !body.closed.Load() {
		t.Fatal("rejected page dispatched or retained its body")
	}
}

func TestCredentialLeaseHTTPGuardClonesSettingsAndRefusesRedirect(t *testing.T) {
	lease, scope, _ := newLeaseFixture(t, nil, nil)
	defer lease.Release()
	var sends, originalRedirects atomic.Int32
	original := &http.Client{Timeout: 17 * time.Second, Transport: leaseHTTPRoundTrip(func(r *http.Request) (*http.Response, error) {
		sends.Add(1)
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://another-issuer.invalid/models"}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: r}, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { originalRedirects.Add(1); return http.ErrUseLastResponse }}
	guarded := lease.GuardHTTPClient(scope, original)
	if guarded == original || guarded.Timeout != original.Timeout || original.Transport == guarded.Transport {
		t.Fatal("guard did not preserve settings in an independent client")
	}
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.invalid/models", nil)
	response, err := guarded.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || sends.Load() != 1 || originalRedirects.Load() != 0 {
		t.Fatal("guard followed a redirect or invoked the injected redirect hook")
	}
	request, _ = http.NewRequest(http.MethodGet, "https://upstream.invalid/models", nil)
	response, err = original.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if originalRedirects.Load() != 1 {
		t.Fatal("guard mutated the injected client's redirect policy")
	}
}

func TestCredentialLeaseHTTPGuardFailsClosedForNilAuthorityAndCancellation(t *testing.T) {
	var missing *CredentialLease
	lease, bound, _ := newLeaseFixture(t, nil, nil)
	defer lease.Release()
	for _, test := range []struct {
		name   string
		lease  *CredentialLease
		cancel bool
	}{{"missing", missing, false}, {"canceled", lease, true}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			called := false
			client := test.lease.GuardHTTPClient(bound, &http.Client{Transport: leaseHTTPRoundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("unexpected send") })})
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://upstream.invalid/models", nil)
			if _, err := client.Do(request); err == nil || called {
				t.Fatal("unavailable authority dispatched an HTTP request")
			}
		})
	}
	defaultClient := lease.GuardHTTPClient(bound, nil)
	guard := defaultClient.Transport.(credentialLeaseRoundTripper)
	if guard.transport != http.DefaultTransport {
		t.Fatal("nil client did not use the standard transport")
	}
}
