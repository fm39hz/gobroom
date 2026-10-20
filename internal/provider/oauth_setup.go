package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

type OAuthSetupMode string

const (
	OAuthSetupAuto   OAuthSetupMode = "auto"
	OAuthSetupCode   OAuthSetupMode = "code"
	OAuthSetupDevice OAuthSetupMode = "device"
)

var oauthMethodInputSchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.auth.oauth2.method-input", ContractVersion: 1}

func registerAuthorizationInputSchemas(runtime *RuntimeRegistry) error {
	if err := runtime.RegisterSchemaDocument(AuthorizationCodeInputSchemaRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","minLength":1,"maxLength":8192},"code":{"type":"string","minLength":1,"maxLength":4096},"state":{"type":"string","minLength":1,"maxLength":256}},"additionalProperties":false,"oneOf":[{"required":["url"],"not":{"anyOf":[{"required":["code"]},{"required":["state"]}]}},{"required":["code","state"],"not":{"required":["url"]}}]}`)); err != nil {
		return err
	}
	return runtime.RegisterSchemaDocument(oauthMethodInputSchemaRef, json.RawMessage(`{"type":"object","properties":{"method":{"enum":["code","device"]}},"required":["method"],"additionalProperties":false}`))
}

func buildOAuthSetup(base OAuthAuth, mode OAuthSetupMode) (AuthFlow, error) {
	if mode != "" && mode != OAuthSetupAuto && mode != OAuthSetupCode && mode != OAuthSetupDevice {
		return nil, fmt.Errorf("invalid OAuth setup mode")
	}
	var code, device AuthFlow
	var err error
	if base.Config.AuthURL != "" && base.Config.RedirectURL != "" {
		code, err = NewAuthorizationCodeDriver(base, AuthorizationCodeOptions{RedirectURL: base.Config.RedirectURL}, base.authorizationCodeURL, base.exchangeCode)
		if err != nil {
			return nil, err
		}
	}
	if base.Config.DeviceAuthURL != "" {
		device, err = NewDeviceAuthorizationDriver(base, base.BeginDeviceAuthorization, base.WaitForDeviceAuthorization)
		if err != nil {
			return nil, err
		}
	}
	switch mode {
	case OAuthSetupCode:
		if code == nil {
			return nil, fmt.Errorf("OAuth code setup is not configured")
		}
		return code, nil
	case OAuthSetupDevice:
		if device == nil {
			return nil, fmt.Errorf("OAuth device setup is not configured")
		}
		return device, nil
	}
	if code != nil && device != nil {
		return oauthSetupChoice{AuthFlow: base, code: code.(AuthorizationFlow), device: device.(AuthorizationFlow)}, nil
	}
	if code != nil {
		return code, nil
	}
	if device != nil {
		return device, nil
	}
	return base, nil
}

type oauthSetupChoice struct {
	AuthFlow
	code, device AuthorizationFlow
}
type oauthSetupChoiceState struct {
	Mode      OAuthSetupMode `json:"mode"`
	Nested    string         `json:"nested"`
	ExpiresAt time.Time      `json:"expiresAt"`
}

func (f oauthSetupChoice) BeginAuthorization(ctx context.Context, _ AuthInput) (AuthorizationTransition, error) {
	if err := ctx.Err(); err != nil {
		return AuthorizationTransition{}, err
	}
	p := oauthSetupChoiceState{ExpiresAt: time.Now().Add(MaxAuthorizationDuration)}
	encoded, err := json.Marshal(p)
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("encode OAuth setup choice")
	}
	state, err := NewAuthorizationState(string(encoded))
	if err != nil {
		return AuthorizationTransition{}, err
	}
	return AuthorizationTransition{State: state, ExpiresAt: p.ExpiresAt, Actions: []AuthorizationAction{{ID: "select_method", Kind: AuthorizationInput, Label: "Choose authorization method: code or device", InputSchemaRef: &oauthMethodInputSchemaRef}}}, nil
}

func (f oauthSetupChoice) AdvanceAuthorization(ctx context.Context, state AuthorizationState, event AuthorizationEvent) (AuthorizationTransition, error) {
	var p oauthSetupChoiceState
	if json.Unmarshal([]byte(state.Encoded()), &p) != nil || !p.ExpiresAt.After(time.Now()) {
		return AuthorizationTransition{}, fmt.Errorf("OAuth setup choice is invalid or expired")
	}
	var next AuthorizationTransition
	var err error
	if p.Mode == "" {
		if event.Kind != AuthorizationSubmit || event.ActionID != "select_method" {
			return AuthorizationTransition{}, fmt.Errorf("OAuth setup method must be selected")
		}
		var choice struct {
			Method OAuthSetupMode `json:"method"`
		}
		if decodeStrictJSON(event.Value, &choice) != nil || (choice.Method != OAuthSetupCode && choice.Method != OAuthSetupDevice) {
			return AuthorizationTransition{}, fmt.Errorf("invalid OAuth setup method")
		}
		p.Mode = choice.Method
		if p.Mode == OAuthSetupCode {
			next, err = f.code.BeginAuthorization(ctx, AuthInput{})
		} else {
			next, err = f.device.BeginAuthorization(ctx, AuthInput{})
		}
	} else {
		nested, stateErr := NewAuthorizationState(p.Nested)
		if stateErr != nil {
			return AuthorizationTransition{}, fmt.Errorf("invalid nested OAuth setup state")
		}
		switch p.Mode {
		case OAuthSetupCode:
			next, err = f.code.AdvanceAuthorization(ctx, nested, event)
		case OAuthSetupDevice:
			next, err = f.device.AdvanceAuthorization(ctx, nested, event)
		default:
			return AuthorizationTransition{}, fmt.Errorf("invalid selected OAuth method")
		}
	}
	if err != nil {
		return AuthorizationTransition{}, err
	}
	if next.Credential != nil || next.Failure != "" {
		return next, nil
	}
	if next.ExpiresAt.After(p.ExpiresAt) {
		next.ExpiresAt = p.ExpiresAt
	} else {
		p.ExpiresAt = next.ExpiresAt
	}
	p.Nested = next.State.Encoded()
	encoded, err := json.Marshal(p)
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("encode selected OAuth setup")
	}
	next.State, err = NewAuthorizationState(string(encoded))
	return next, err
}
