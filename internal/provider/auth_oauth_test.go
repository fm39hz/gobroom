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
