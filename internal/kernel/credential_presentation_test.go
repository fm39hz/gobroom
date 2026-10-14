package kernel

import (
	"net/http"
	"testing"
)

func TestCredentialPresentationAppliesTokenAndProtocolHeaders(t *testing.T) {
	headers := make(http.Header)
	credential := Credential{Secret: "oauth-token"}
	presentation := CredentialPresentation{Header: "Authorization", Prefix: "Bearer ", Headers: map[string]string{"anthropic-beta": "oauth-2025-04-20", "anthropic-version": "2023-06-01"}}
	if err := ApplyCredentialPresentation(headers, credential, presentation); err != nil {
		t.Fatal(err)
	}
	if headers.Get("Authorization") != "Bearer oauth-token" || headers.Get("anthropic-beta") != "oauth-2025-04-20" || headers.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("presented headers=%#v", headers)
	}
}

func TestCredentialPresentationRejectsSecretsInStaticHeaders(t *testing.T) {
	for _, header := range []string{"Authorization", "Cookie", "X-API-Key", "X-Auth-Token", "X-Provider-Secret"} {
		err := (CredentialPresentation{Header: "Authorization", Prefix: "Bearer ", Headers: map[string]string{header: "must-not-be-static"}}).Validate()
		if err == nil {
			t.Fatalf("static credential header %q was accepted", header)
		}
	}
}

func TestCredentialPresentationRequiresTokenHeader(t *testing.T) {
	err := ApplyCredentialPresentation(make(http.Header), Credential{Secret: "secret"}, CredentialPresentation{})
	if err == nil {
		t.Fatal("secret without a declared presentation header was accepted")
	}
}

func TestCredentialPresentationRejectsTransportControlHeaders(t *testing.T) {
	for _, header := range []string{"Host", "Cookie", "Content-Length", "Transfer-Encoding"} {
		if err := (CredentialPresentation{Header: header}).Validate(); err == nil {
			t.Fatalf("transport control header %q was accepted for credential presentation", header)
		}
	}
}
