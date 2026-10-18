package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestAuthorizationStateBoundsAndRedaction(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("x", MaxAuthorizationStateBytes+1)} {
		if _, err := NewAuthorizationState(value); err == nil {
			t.Fatalf("accepted state length %d", len(value))
		}
	}
	secret := "private-authorization-state"
	state, err := NewAuthorizationState(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{state.String(), fmt.Sprintf("%#v", state)} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("state leaked in %q", rendered)
		}
	}
	data, err := json.Marshal(state)
	if err != nil || string(data) != `"[redacted]"` {
		t.Fatalf("MarshalJSON = %s, %v", data, err)
	}
}

func TestAuthorizationTransitionTerminalAndLimits(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	state, _ := NewAuthorizationState("state")
	credential, _ := NewCredentialState("credential")
	for name, transition := range map[string]AuthorizationTransition{
		"valid completion":            {Credential: &credential},
		"mixed terminal":              {Credential: &credential, State: state},
		"missing continuation action": {State: state},
		"expired":                     {State: state, ExpiresAt: now},
		"too long expiry":             {State: state, ExpiresAt: now.Add(MaxAuthorizationDuration + time.Nanosecond), PollAfter: time.Second},
		"poll too short":              {State: state, ExpiresAt: now.Add(time.Minute), PollAfter: time.Millisecond},
		"poll too long":               {State: state, ExpiresAt: now.Add(time.Minute), PollAfter: time.Minute + time.Nanosecond},
		"too many actions":            {State: state, ExpiresAt: now.Add(time.Minute), Actions: make([]AuthorizationAction, 9)},
	} {
		err := transition.Validate(now)
		if name == "valid completion" && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if name != "valid completion" && err == nil {
			t.Errorf("%s unexpectedly validated", name)
		}
	}
}

func TestAuthorizationTransitionTypedFailureIsTerminal(t *testing.T) {
	now := time.Now()
	for _, failure := range []AuthorizationFailure{AuthorizationDenied, AuthorizationExpired, AuthorizationUnavailable} {
		if err := (AuthorizationTransition{Failure: failure}).Validate(now); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := NewAuthorizationState("private")
	credential, _ := NewCredentialState("secret")
	for _, transition := range []AuthorizationTransition{{Failure: "unknown"}, {Failure: AuthorizationDenied, State: state}, {Failure: AuthorizationDenied, Credential: &credential}, {Failure: AuthorizationDenied, PollAfter: time.Second}} {
		if err := transition.Validate(now); err == nil {
			t.Fatal("accepted unknown or mixed terminal failure")
		}
	}
}

func TestAuthorizationActionShapes(t *testing.T) {
	now := time.Now()
	state, _ := NewAuthorizationState("state")
	base := AuthorizationTransition{State: state, ExpiresAt: now.Add(time.Minute)}
	valid := []AuthorizationAction{
		{ID: "visit", Kind: AuthorizationOpenURL, Label: "Continue", URL: "https://example.test/authorize"},
		{ID: "code", Kind: AuthorizationDisplayCode, Label: "Enter this code", Code: "ABCD"},
		{ID: "input", Kind: AuthorizationInput, Label: "Provide details", InputSchemaRef: &extensions.Ref{Kind: extensions.SchemaKind, ID: "authorize", ContractVersion: 7}},
	}
	for i, action := range valid {
		transition := base
		transition.Actions = []AuthorizationAction{action}
		if err := transition.Validate(now); err != nil {
			t.Errorf("valid action %d: %v", i, err)
		}
	}
	invalid := []AuthorizationAction{
		{ID: "url", Kind: AuthorizationOpenURL, Label: "go", URL: "javascript:alert(1)"},
		{ID: "url", Kind: AuthorizationOpenURL, Label: "go", URL: "https://user@example.test/"},
		{ID: "url", Kind: AuthorizationOpenURL, Label: "go", URL: "https://example.test/#fragment"},
		{ID: "code", Kind: AuthorizationDisplayCode, Label: "code", Code: "x", URL: "https://example.test"},
		{ID: "bad\nID", Kind: AuthorizationDisplayCode, Label: "code", Code: "x"},
		{ID: "input", Kind: AuthorizationInput, Label: "input", InputSchemaRef: &extensions.Ref{Kind: "action", ID: "form", ContractVersion: 1}},
		{ID: "input", Kind: AuthorizationInput, Label: "input", InputSchemaRef: &extensions.Ref{Kind: extensions.SchemaKind, ID: "form", ContractVersion: 0}},
		{ID: "bad", Kind: AuthorizationDisplayCode, Label: "", Code: "x"},
	}
	for i, action := range invalid {
		transition := base
		transition.Actions = []AuthorizationAction{action}
		if err := transition.Validate(now); err == nil {
			t.Errorf("invalid action %d unexpectedly validated", i)
		}
	}
	transition := base
	transition.Actions = []AuthorizationAction{valid[0], valid[0]}
	if err := transition.Validate(now); err == nil {
		t.Error("duplicate action IDs unexpectedly validated")
	}
}
