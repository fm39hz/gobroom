package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const MaxAuthorizationStateBytes = 64 << 10

// AuthorizationState is opaque provider-owned state for an interactive flow.
type AuthorizationState struct{ encoded string }

func NewAuthorizationState(encoded string) (AuthorizationState, error) {
	s := AuthorizationState{encoded: encoded}
	return s, s.Validate()
}

func (s AuthorizationState) Validate() error {
	if len(s.encoded) == 0 || len(s.encoded) > MaxAuthorizationStateBytes {
		return fmt.Errorf("authorization state must contain 1..%d bytes", MaxAuthorizationStateBytes)
	}
	return nil
}

func (s AuthorizationState) Encoded() string            { return s.encoded }
func (AuthorizationState) String() string               { return "[redacted]" }
func (AuthorizationState) GoString() string             { return "provider.AuthorizationState{[redacted]}" }
func (AuthorizationState) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// AuthorizationFlow is an optional capability implemented alongside AuthFlow.
type AuthorizationFlow interface {
	BeginAuthorization(context.Context, AuthInput) (AuthorizationTransition, error)
	AdvanceAuthorization(context.Context, AuthorizationState, AuthorizationEvent) (AuthorizationTransition, error)
}

type AuthorizationActionKind string

const (
	AuthorizationOpenURL     AuthorizationActionKind = "open_url"
	AuthorizationDisplayCode AuthorizationActionKind = "display_code"
	AuthorizationInput       AuthorizationActionKind = "input"
)

type AuthorizationAction struct {
	ID             string                  `json:"id"`
	Kind           AuthorizationActionKind `json:"kind"`
	Label          string                  `json:"label"`
	URL            string                  `json:"url,omitempty"`
	Code           string                  `json:"code,omitempty"`
	InputSchemaRef *extensions.Ref         `json:"inputSchemaRef,omitempty"`
}

type AuthorizationEventKind string

const (
	AuthorizationSubmit AuthorizationEventKind = "submit"
	AuthorizationPoll   AuthorizationEventKind = "poll"
)

type AuthorizationEvent struct {
	Kind     AuthorizationEventKind `json:"kind"`
	ActionID string                 `json:"actionId"`
	Value    json.RawMessage        `json:"value,omitempty"`
}

type AuthorizationTransition struct {
	Failure    AuthorizationFailure  `json:"failure,omitempty"`
	State      AuthorizationState    `json:"-"`
	Credential *CredentialState      `json:"-"`
	Actions    []AuthorizationAction `json:"actions,omitempty"`
	PollAfter  time.Duration         `json:"-"`
	ExpiresAt  time.Time             `json:"expiresAt"`
}

type AuthorizationFailure string

const (
	AuthorizationDenied      AuthorizationFailure = "denied"
	AuthorizationExpired     AuthorizationFailure = "expired"
	AuthorizationUnavailable AuthorizationFailure = "unavailable"
)

func (t AuthorizationTransition) Validate(now time.Time) error {
	if t.Failure != "" {
		if t.Failure != AuthorizationDenied && t.Failure != AuthorizationExpired && t.Failure != AuthorizationUnavailable {
			return fmt.Errorf("unknown authorization failure")
		}
		if t.Credential != nil || t.State.Encoded() != "" || len(t.Actions) != 0 || t.PollAfter != 0 {
			return fmt.Errorf("failed authorization cannot contain credentials, state, actions or polling")
		}
		return nil
	}
	if t.Credential != nil {
		if err := t.Credential.Validate(); err != nil {
			return fmt.Errorf("authorization credential: %w", err)
		}
		if t.State.Encoded() != "" || len(t.Actions) != 0 || t.PollAfter != 0 {
			return fmt.Errorf("completed authorization cannot contain state, actions or polling")
		}
		return nil
	}
	if err := t.State.Validate(); err != nil {
		return fmt.Errorf("authorization transition state: %w", err)
	}
	if t.ExpiresAt.IsZero() || !t.ExpiresAt.After(now) || t.ExpiresAt.After(now.Add(10*time.Minute)) {
		return fmt.Errorf("authorization expiry must be in the future and within 10 minutes")
	}
	if len(t.Actions) > 8 {
		return fmt.Errorf("authorization transition exceeds 8 actions")
	}
	if t.PollAfter != 0 && (t.PollAfter < 100*time.Millisecond || t.PollAfter > time.Minute) {
		return fmt.Errorf("authorization poll interval must be 100ms..1m")
	}
	if len(t.Actions) == 0 && t.PollAfter == 0 {
		return fmt.Errorf("continuing authorization requires an action or poll interval")
	}
	seen := make(map[string]struct{}, len(t.Actions))
	for _, action := range t.Actions {
		if action.ID == "" || len(action.ID) > 128 {
			return fmt.Errorf("authorization action ID must contain 1..128 bytes")
		}
		if err := (extensions.Ref{Kind: "action", ID: action.ID, ContractVersion: 1}).Validate(); err != nil {
			return fmt.Errorf("authorization action ID: %w", err)
		}
		if _, ok := seen[action.ID]; ok {
			return fmt.Errorf("duplicate authorization action ID %q", action.ID)
		}
		seen[action.ID] = struct{}{}
		if action.Label == "" || len(action.Label) > 256 {
			return fmt.Errorf("authorization action label must contain 1..256 bytes")
		}
		if action.Kind != AuthorizationOpenURL && action.Kind != AuthorizationDisplayCode && action.Kind != AuthorizationInput {
			return fmt.Errorf("invalid authorization action kind %q", action.Kind)
		}
		if action.InputSchemaRef != nil {
			if err := action.InputSchemaRef.Validate(); err != nil {
				return fmt.Errorf("authorization input schema: %w", err)
			}
			if action.InputSchemaRef.Kind != extensions.SchemaKind {
				return fmt.Errorf("authorization input schema requires a schema reference")
			}
		}
		switch action.Kind {
		case AuthorizationOpenURL:
			if len(action.URL) == 0 || len(action.URL) > 8192 || action.Code != "" || action.InputSchemaRef != nil {
				return fmt.Errorf("open_url action has invalid or mixed fields")
			}
			u, err := url.Parse(action.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("open_url action requires an absolute HTTP/S URL without userinfo or fragment")
			}
		case AuthorizationDisplayCode:
			if action.Code == "" || len(action.Code) > 256 || action.URL != "" || action.InputSchemaRef != nil {
				return fmt.Errorf("display_code action has invalid or mixed fields")
			}
		case AuthorizationInput:
			if action.InputSchemaRef == nil || action.Code != "" || action.URL != "" {
				return fmt.Errorf("input action requires only an input schema reference")
			}
		}
	}
	return nil
}
