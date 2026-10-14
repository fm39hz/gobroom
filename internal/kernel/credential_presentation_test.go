package kernel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCredentialPresentationAppliesTokenAndProtocolHeaders(t *testing.T) {
	headers := make(http.Header)
	credential := Credential{Secret: "oauth-token", RequestHeaders: http.Header{"x-msh-device-id": []string{"device-123"}, "x-msh-platform": []string{"CLIProxyAPI"}}}
	presentation := CredentialPresentation{Header: "Authorization", Prefix: "Bearer ", Headers: map[string]string{"anthropic-beta": "oauth-2025-04-20", "anthropic-version": "2023-06-01", "X-Msh-Platform": "static-default"}}
	if err := ApplyCredentialPresentation(headers, credential, presentation); err != nil {
		t.Fatal(err)
	}
	if headers.Get("Authorization") != "Bearer oauth-token" || headers.Get("anthropic-beta") != "oauth-2025-04-20" || headers.Get("anthropic-version") != "2023-06-01" || headers.Get("X-Msh-Device-Id") != "device-123" || headers.Get("X-Msh-Platform") != "CLIProxyAPI" {
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

func TestCredentialAuthContextIsBoundedAndControlFree(t *testing.T) {
	if err := (Credential{AuthContext: AuthContext{"userId": "account-1", "machineId": "machine-1"}}).ValidateRuntimeContext(); err != nil {
		t.Fatal(err)
	}
	if err := (Credential{AuthContext: AuthContext{"userId": "bad\r\nheader"}}).ValidateRuntimeContext(); err == nil {
		t.Fatal("control characters were accepted in auth context")
	}
	if err := (Credential{AuthContext: AuthContext{"bad\nkey": "value"}}).ValidateRuntimeContext(); err == nil {
		t.Fatal("control characters were accepted in auth context key")
	}
}

func TestCredentialRuntimeContextIsNotSerialized(t *testing.T) {
	encoded, err := json.Marshal(Credential{Secret: "secret", AuthContext: AuthContext{"userId": "private-user-marker"}, RequestHeaders: http.Header{"X-Private": []string{"private-header-marker"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-user-marker") || strings.Contains(string(encoded), "private-header-marker") {
		t.Fatalf("runtime auth context leaked through JSON serialization: %s", encoded)
	}
}
