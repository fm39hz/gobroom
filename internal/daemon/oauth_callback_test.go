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
			_, _, ok := loopbackCallbackAddress(parsed)
			if ok != test.wantReady {
				t.Fatalf("loopback listener eligibility=%v, want %v", ok, test.wantReady)
			}
		})
	}
}

func TestOAuthCallbackTargetMatchesExactRegisteredPathAndQuery(t *testing.T) {
	expected := "http://127.0.0.1:12345/oauth/callback?tenant=local"
	if !sameOAuthCallbackTarget(expected, expected+"&code=abc&state=xyz") {
		t.Fatal("registered callback target with OAuth response parameters did not match")
	}
	for _, actual := range []string{
		"http://attacker.test:12345/oauth/callback?tenant=local&code=abc&state=xyz",
		"http://127.0.0.1:12345/other?tenant=local&code=abc&state=xyz",
		"http://127.0.0.1:12345/oauth/callback?tenant=other&code=abc&state=xyz",
		"http://127.0.0.1:12345/oauth/callback?tenant=local&code=abc&state=xyz&next=https://attacker.test",
	} {
		if sameOAuthCallbackTarget(expected, actual) {
			t.Fatalf("unbound callback target accepted: %s", actual)
		}
	}
}
