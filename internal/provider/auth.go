package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

type AuthFlowFactory func(AuthOptions) (AuthFlow, error)

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

type NoAuth struct{}

func (NoAuth) ID() string               { return "none" }
func (NoAuth) SetupSchema() SetupSchema { return SetupSchema{} }
func (NoAuth) Resolve(_ context.Context, input AuthInput) (kernel.Credential, error) {
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type}, nil
}
func (NoAuth) Refresh(_ context.Context, credential kernel.Credential) (kernel.Credential, error) {
	return credential, nil
}

type OAuthAuth struct{ Config appauth.OAuthConfig }

type OAuthTokenState struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
}

func DecodeOAuthTokenState(raw string) (OAuthTokenState, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return OAuthTokenState{}, fmt.Errorf("oauth token is required")
	}
	if strings.HasPrefix(trimmed, "{") {
		var state OAuthTokenState
		if err := json.Unmarshal([]byte(trimmed), &state); err != nil {
			return OAuthTokenState{}, fmt.Errorf("decode OAuth connection token state: %w", err)
		}
		if state.AccessToken == "" {
			return OAuthTokenState{}, fmt.Errorf("OAuth token state has no access_token")
		}
		return state, nil
	}
	return OAuthTokenState{AccessToken: raw}, nil
}

func EncodeOAuthCredential(credential kernel.Credential) (string, error) {
	return encodeOAuthTokenState(OAuthTokenState{
		AccessToken: credential.Secret, RefreshToken: credential.RefreshToken,
		ExpiresAt: credential.ExpiresAt, ClientID: credential.ClientID,
		ClientSecret: credential.ClientSecret,
	})
}

func encodeOAuthTokenState(state OAuthTokenState) (string, error) {
	if state.AccessToken == "" {
		return "", fmt.Errorf("OAuth token state has no access_token")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (OAuthAuth) ID() string { return "oauth2" }
func (OAuthAuth) SetupSchema() SetupSchema {
	return SetupSchema{Fields: []SetupField{{Name: "access_token", Type: "string", Required: true, Secret: true}, {Name: "refresh_token", Type: "string", Secret: true}, {Name: "client_secret", Type: "string", Secret: true}, {Name: "expires_at", Type: "datetime"}}}
}

func OAuthAuthFactory(options AuthOptions) (AuthFlow, error) {
	if options.OAuth == nil || options.OAuth.ClientID == "" || options.OAuth.AuthURL == "" || options.OAuth.TokenURL == "" {
		return nil, fmt.Errorf("oauth2 requires clientId, authUrl and tokenUrl in authOptions.oauth")
	}
	config := appauth.OAuthConfig{ClientID: options.OAuth.ClientID, AuthURL: options.OAuth.AuthURL, TokenURL: options.OAuth.TokenURL, Scopes: append([]string(nil), options.OAuth.Scopes...), RedirectURL: options.OAuth.RedirectURL}
	return OAuthAuth{Config: config}, nil
}

func (a OAuthAuth) Resolve(_ context.Context, input AuthInput) (kernel.Credential, error) {
	state, err := DecodeOAuthTokenState(input.Secret)
	if err != nil {
		return kernel.Credential{}, err
	}
	clientID := state.ClientID
	if clientID == "" {
		clientID = a.Config.ClientID
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.AccessToken, RefreshToken: state.RefreshToken, ExpiresAt: state.ExpiresAt, ClientID: clientID, ClientSecret: state.ClientSecret}, nil
}

func (a OAuthAuth) Refresh(ctx context.Context, credential kernel.Credential) (kernel.Credential, error) {
	if credential.RefreshToken == "" {
		return kernel.Credential{}, fmt.Errorf("oauth refresh token is missing")
	}
	config := a.Config
	if credential.ClientID != "" {
		config.ClientID = credential.ClientID
	}
	if credential.ClientSecret != "" {
		config.ClientSecret = credential.ClientSecret
	}
	token := config.Config().TokenSource(ctx, &oauth2.Token{AccessToken: credential.Secret, RefreshToken: credential.RefreshToken, Expiry: credential.ExpiresAt})
	refreshed, err := token.Token()
	if err != nil {
		return kernel.Credential{}, err
	}
	return kernel.Credential{ConnectionID: credential.ConnectionID, Type: credential.Type, Secret: refreshed.AccessToken, RefreshToken: refreshed.RefreshToken, ExpiresAt: refreshed.Expiry, ClientID: config.ClientID, ClientSecret: config.ClientSecret}, nil
}

type AuthRegistry struct {
	flows     map[string]AuthFlow
	factories map[string]AuthFlowFactory
}

func NewAuthRegistry() *AuthRegistry {
	registry := &AuthRegistry{flows: map[string]AuthFlow{}, factories: map[string]AuthFlowFactory{}}
	registry.Register(StaticSecretAuth{})
	registry.Register(NoAuth{})
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

func (r *AuthRegistry) RegisterFactory(id string, factory AuthFlowFactory) error {
	if r == nil || id == "" || factory == nil {
		return fmt.Errorf("auth factory and ID are required")
	}
	if _, exists := r.factories[id]; exists {
		return fmt.Errorf("auth factory %q is already registered", id)
	}
	if _, exists := r.flows[id]; exists {
		return fmt.Errorf("auth flow %q is already registered as an instance", id)
	}
	r.factories[id] = factory
	return nil
}

func (r *AuthRegistry) Build(id string, options AuthOptions) (AuthFlow, error) {
	if r == nil {
		return nil, fmt.Errorf("auth registry is not initialized")
	}
	if factory, ok := r.factories[id]; ok {
		return factory(options)
	}
	if flow, ok := r.flows[id]; ok {
		if options.OAuth != nil {
			return nil, fmt.Errorf("authOptions.oauth cannot be applied to auth flow %q", id)
		}
		return flow, nil
	}
	return nil, fmt.Errorf("auth flow %q is not registered", id)
}
