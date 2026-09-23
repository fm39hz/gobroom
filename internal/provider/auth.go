package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appauth "github.com/fm39hz/gobroom/internal/auth"
	"github.com/fm39hz/gobroom/internal/kernel"
	"golang.org/x/oauth2"
)

type SetupField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Description string `json:"description,omitempty"`
}

type SetupSchema struct {
	Fields []SetupField `json:"fields"`
}

type AuthInput struct {
	ConnectionID string
	Type         string
	Secret       string
	Metadata     map[string]any
}

type AuthFlow interface {
	ID() string
	SetupSchema() SetupSchema
	Resolve(context.Context, AuthInput) (kernel.Credential, error)
	Refresh(context.Context, kernel.Credential) (kernel.Credential, error)
}

type StaticSecretAuth struct{}

func (StaticSecretAuth) ID() string { return "static-secret" }
func (StaticSecretAuth) SetupSchema() SetupSchema {
	return SetupSchema{Fields: []SetupField{{Name: "secret", Type: "string", Required: true, Secret: true, Description: "API key or bearer secret"}}}
}
func (StaticSecretAuth) Resolve(_ context.Context, input AuthInput) (kernel.Credential, error) {
	if input.Secret == "" {
		return kernel.Credential{}, fmt.Errorf("secret is required")
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (StaticSecretAuth) Refresh(_ context.Context, credential kernel.Credential) (kernel.Credential, error) {
	return credential, nil
}

type OAuthAuth struct{ Config appauth.OAuthConfig }

func (OAuthAuth) ID() string { return "oauth2" }
func (OAuthAuth) SetupSchema() SetupSchema {
	return SetupSchema{Fields: []SetupField{{Name: "access_token", Type: "string", Required: true, Secret: true}, {Name: "refresh_token", Type: "string", Secret: true}, {Name: "expires_at", Type: "datetime"}}}
}

func (a OAuthAuth) Resolve(_ context.Context, input AuthInput) (kernel.Credential, error) {
	if input.Secret == "" {
		return kernel.Credential{}, fmt.Errorf("oauth token is required")
	}
	var state struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if json.Unmarshal([]byte(input.Secret), &state) == nil && state.AccessToken != "" {
		return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.AccessToken, RefreshToken: state.RefreshToken, ExpiresAt: state.ExpiresAt}, nil
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}

func (a OAuthAuth) Refresh(ctx context.Context, credential kernel.Credential) (kernel.Credential, error) {
	if credential.RefreshToken == "" {
		return kernel.Credential{}, fmt.Errorf("oauth refresh token is missing")
	}
	token := (&oauth2.Config{ClientID: a.Config.ClientID, ClientSecret: a.Config.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: a.Config.TokenURL}}).TokenSource(ctx, &oauth2.Token{AccessToken: credential.Secret, RefreshToken: credential.RefreshToken, Expiry: credential.ExpiresAt})
	refreshed, err := token.Token()
	if err != nil {
		return kernel.Credential{}, err
	}
	return kernel.Credential{ConnectionID: credential.ConnectionID, Type: credential.Type, Secret: refreshed.AccessToken, RefreshToken: refreshed.RefreshToken, ExpiresAt: refreshed.Expiry}, nil
}

type AuthRegistry struct {
	flows map[string]AuthFlow
}

func NewAuthRegistry() *AuthRegistry {
	registry := &AuthRegistry{flows: map[string]AuthFlow{}}
	registry.Register(StaticSecretAuth{})
	return registry
}

func (r *AuthRegistry) Register(flow AuthFlow) error {
	if flow == nil || flow.ID() == "" {
		return fmt.Errorf("auth flow and ID are required")
	}
	if _, exists := r.flows[flow.ID()]; exists {
		return fmt.Errorf("auth flow %q already registered", flow.ID())
	}
	r.flows[flow.ID()] = flow
	return nil
}

func (r *AuthRegistry) Resolve(id string) (AuthFlow, bool) {
	flow, ok := r.flows[id]
	return flow, ok
}
