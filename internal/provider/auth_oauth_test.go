package provider

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/auth"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestOAuthAuthParsesTokenState(t *testing.T) {
	flow := OAuthAuth{Config: auth.OAuthConfig{TokenURL: "https://oauth.test/token"}}
	secret, _ := json.Marshal(map[string]any{"access_token": "access", "refresh_token": "refresh"})
	credential, err := flow.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: string(secret)})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "access" || credential.RefreshToken != "refresh" {
		t.Fatalf("credential=%#v", credential)
	}
}

func TestOAuthTokenStateUsesTypedJSONAndRejectsMalformedObjects(t *testing.T) {
	if _, err := DecodeOAuthTokenState(`{"access_token":`); err == nil {
		t.Fatal("malformed OAuth token object must not be treated as a raw access token")
	}
	if _, err := DecodeOAuthTokenState(`{"refresh_token":"refresh"}`); err == nil {
		t.Fatal("token object without access_token must be rejected")
	}
	plain, err := DecodeOAuthTokenState("plain-access-token")
	if err != nil || plain.AccessToken != "plain-access-token" {
		t.Fatalf("plain token state=%#v err=%v", plain, err)
	}
	credential := kernel.Credential{Secret: "access", RefreshToken: "refresh", ClientID: "client", ClientSecret: "private", ExpiresAt: time.Now().UTC().Round(time.Second)}
	encoded, err := EncodeOAuthCredential(credential)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOAuthTokenState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.AccessToken != credential.Secret || decoded.RefreshToken != credential.RefreshToken || decoded.ClientID != credential.ClientID || decoded.ClientSecret != credential.ClientSecret || !decoded.ExpiresAt.Equal(credential.ExpiresAt) {
		t.Fatalf("OAuth state roundtrip=%#v", decoded)
	}
}

func TestOAuthAuthRefreshesAgainstTokenEndpoint(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh" {
			t.Errorf("form=%v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable: %v", err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	defer server.Close()
	flow := OAuthAuth{Config: auth.OAuthConfig{ClientID: "client", ClientSecret: "secret", TokenURL: "http://" + listener.Addr().String()}}
	credential, err := flow.Refresh(context.Background(), kernel.Credential{ConnectionID: "conn", Type: "oauth2", Secret: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "new-access" || credential.RefreshToken != "new-refresh" || !credential.ExpiresAt.After(time.Now()) {
		t.Fatalf("credential=%#v", credential)
	}
}

func TestDefinitionBoundOAuthFlowRefreshesWithConnectionSecrets(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok || clientID != "public-client" || clientSecret != "connection-secret" {
			t.Errorf("client authentication = %q/%q, ok=%v", clientID, clientSecret, ok)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh" {
			t.Errorf("form=%v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable: %v", err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	defer server.Close()

	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{
		ID: "bound-oauth", Version: "1", DisplayName: "Bound OAuth",
		Auth:        PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2"},
		AuthOptions: AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "public-client", AuthURL: "https://oauth.test/authorize", TokenURL: "http://" + listener.Addr().String()}},
		Operations: map[Operation]OperationBinding{OperationChat: {
			Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json"}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http"},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json"}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse"},
		}},
	}
	manifest, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.DefaultCredentialTypes()["bound-oauth"]; got != "oauth2" {
		t.Fatalf("definition credential type default=%q", got)
	}
	binding := bindings[RuntimeBindingKey("bound-oauth", OperationChat)]
	flows := registry.AuthFlowsForBindings(bindings)
	if flow := flows[AuthBindingKey("bound-oauth", "oauth2")]; flow == nil || flow.ID() != "oauth2" {
		t.Fatalf("definition-specific OAuth flow was not published to the daemon runtime: %#v", flow)
	}
	state := `{"access_token":"old-access","refresh_token":"refresh","client_secret":"connection-secret","expires_at":"2000-01-01T00:00:00Z"}`
	credential, err := binding.Auth.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: state})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := binding.Auth.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Secret != "new-access" || refreshed.RefreshToken != "new-refresh" || refreshed.ClientID != "public-client" || refreshed.ClientSecret != "connection-secret" {
		t.Fatalf("refreshed credential=%#v", refreshed)
	}
}
