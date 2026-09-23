package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/auth"
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
