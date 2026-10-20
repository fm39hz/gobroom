package daemon

import (
	"net/url"
	"testing"
)

func TestLoopbackCallbackAddressRejectsNonLocalAndInvalidRedirects(t *testing.T) {
	tests := []struct {
		name      string
		redirect  string
		wantReady bool
	}{
		{name: "IPv4 loopback", redirect: "http://127.0.0.1:12345/oauth/callback", wantReady: true},
		{name: "IPv6 loopback", redirect: "http://[::1]:12345/oauth/callback", wantReady: true},
		{name: "localhost", redirect: "http://localhost:12345/oauth/callback", wantReady: true},
		{name: "public host", redirect: "http://auth.example:12345/oauth/callback"},
		{name: "wildcard", redirect: "http://0.0.0.0:12345/oauth/callback"},
		{name: "HTTPS listener requires external TLS", redirect: "https://127.0.0.1:12345/oauth/callback"},
		{name: "ephemeral port cannot match registration", redirect: "http://127.0.0.1:0/oauth/callback"},
		{name: "fragment is not sent to callback server", redirect: "http://127.0.0.1:12345/oauth/callback#complete"},
		{name: "userinfo is forbidden", redirect: "http://user@127.0.0.1:12345/oauth/callback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.redirect)
			if err != nil {
				t.Fatal(err)
			}
			_, _, ok := loopbackCallbackAddress(parsed, false)
			if ok != test.wantReady {
				t.Fatalf("loopback listener eligibility=%v, want %v", ok, test.wantReady)
			}
		})
	}
}
