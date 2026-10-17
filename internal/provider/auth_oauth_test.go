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
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestOAuthAuthParsesTokenState(t *testing.T) {
	flow := OAuthAuth{Config: auth.OAuthConfig{TokenURL: "https://oauth.test/token"}}
	secret, _ := json.Marshal(map[string]any{"access_token": "access", "refresh_token": "refresh"})
	credential, err := flow.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: string(secret)})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Secret != "access" {
		t.Fatalf("credential=%#v", credential)
	}
	if !credential.ExpiresAt.IsZero() {
		t.Fatalf("runtime credential expiry=%s", credential.ExpiresAt)
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
	state := OAuthTokenState{AccessToken: "access", RefreshToken: "refresh", ClientID: "client", ClientSecret: "private", ExpiresAt: time.Now().UTC().Round(time.Second)}
	encoded, err := EncodeOAuthTokenState(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOAuthTokenState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != state {
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
	for name, expiry := range map[string]time.Time{"expired": time.Now().Add(-time.Hour), "not expired": time.Now().Add(time.Hour), "no expiry": {}} {
		t.Run(name, func(t *testing.T) {
			inputState := OAuthTokenState{AccessToken: "old", RefreshToken: "refresh", ClientID: "client", ClientSecret: "secret", ExpiresAt: expiry}
			encoded, err := EncodeOAuthTokenState(inputState)
			if err != nil {
				t.Fatal(err)
			}
			input := AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: encoded}
			state, err := flow.Refresh(context.Background(), input, kernel.Credential{ConnectionID: "conn", Type: "oauth2", Secret: "old", ExpiresAt: expiry})
			if err != nil {
				t.Fatal(err)
			}
			credential, err := flow.Resolve(context.Background(), AuthInput{ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.Encoded()})
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := DecodeOAuthTokenState(state.Encoded())
			if err != nil {
				t.Fatal(err)
			}
			if credential.Secret != "new-access" || !credential.ExpiresAt.Equal(persisted.ExpiresAt) || persisted.AccessToken != "new-access" || persisted.RefreshToken != "new-refresh" || !persisted.ExpiresAt.After(time.Now()) {
				t.Fatal("explicit refresh did not replace the rejected access token")
			}
		})
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
	definition := ProviderDefinition{ContractVersion: 1, ID: "bound-oauth", Version: "1", DisplayName: "Bound OAuth",
		DefaultCredentialType: "oauth2",
		AuthModes:             []AuthModeDefinition{{CredentialType: "oauth2", Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2", ContractVersion: 1}, AuthOptions: AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "public-client", AuthURL: "https://oauth.test/authorize", TokenURL: "http://" + listener.Addr().String()}}}},
		Operations: map[Operation]OperationBinding{OperationChat: {
			TaskRef:  OperationRef(normalize.OperationChatGenerate, 1),
			Endpoint: PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1}, Transport: PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec: PrimitiveRef{Kind: PrimitiveRequestCodec, ID: "openai-chat-json", ContractVersion: 1}, ResponseDecoder: PrimitiveRef{Kind: PrimitiveResponseDecoder, ID: "openai-sse", ContractVersion: 1},
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
	definitionRef := ProviderDefinitionRef("bound-oauth", 1)
	if got := registry.DefaultCredentialTypes()[definitionRef]; got != "oauth2" {
		t.Fatalf("definition credential type default=%q", got)
	}
	binding := bindings[RuntimeBindingKey(definitionRef, OperationChat)]
	flows := registry.AuthFlowsForBindings(bindings)
	if flow := flows[AuthBindingKey(definitionRef, "oauth2", PrimitiveRef{Kind: PrimitiveAuth, ID: "oauth2", ContractVersion: 1})]; flow == nil || flow.ID() != "oauth2" {
		t.Fatalf("definition-specific OAuth flow was not published to the daemon runtime: %#v", flow)
	}
	state := `{"access_token":"old-access","refresh_token":"refresh","client_secret":"connection-secret","expires_at":"2000-01-01T00:00:00Z"}`
	credential, err := binding.AuthModes["oauth2"].Flow.Resolve(context.Background(), AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: state})
	if err != nil {
		t.Fatal(err)
	}
	input := AuthInput{ConnectionID: "conn", Type: "oauth2", Secret: state}
	replacement, err := binding.AuthModes["oauth2"].Flow.Refresh(context.Background(), input, credential)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := binding.AuthModes["oauth2"].Flow.Resolve(context.Background(), AuthInput{ConnectionID: input.ConnectionID, Type: input.Type, Secret: replacement.Encoded()})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := DecodeOAuthTokenState(replacement.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Secret != "new-access" || !refreshed.ExpiresAt.Equal(persisted.ExpiresAt) || persisted.AccessToken != "new-access" || persisted.RefreshToken != "new-refresh" || persisted.ClientID != "public-client" || persisted.ClientSecret != "connection-secret" {
		t.Fatalf("refreshed credential=%#v", refreshed)
	}
}
