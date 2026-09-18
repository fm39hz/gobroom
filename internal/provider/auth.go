package provider

import (
	"context"
	"fmt"

	"github.com/fm39hz/gobroom/internal/kernel"
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
