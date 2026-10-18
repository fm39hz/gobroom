package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type deviceAuthorizationState struct {
	DeviceCode              string          `json:"device_code"`
	UserCode                string          `json:"user_code"`
	VerificationURL         string          `json:"verification_url"`
	VerificationURLComplete string          `json:"verification_url_complete,omitempty"`
	ExpiresAt               time.Time       `json:"expires_at"`
	PollInterval            time.Duration   `json:"poll_interval"`
	PrivateState            json.RawMessage `json:"private_state,omitempty"`
}

type deviceAuthorizationAuthFlow struct {
	AuthFlow
	begin func(context.Context, AuthInput) (DeviceAuthorization, error)
	await func(context.Context, DeviceAuthorization) (CredentialState, error)
}

// NewDeviceAuthorizationDriver adapts provider-owned RFC 8628 requests to the
// daemon's generic authorization lifecycle. The daemon owns the cancellable
// background wait; the protocol implementation owns provider polling semantics.
func NewDeviceAuthorizationDriver(auth AuthFlow, begin func(context.Context, AuthInput) (DeviceAuthorization, error), await func(context.Context, DeviceAuthorization) (CredentialState, error)) (AuthFlow, error) {
	if auth == nil || begin == nil || await == nil {
		return nil, fmt.Errorf("device authorization requires an auth flow and begin/await handlers")
	}
	return deviceAuthorizationAuthFlow{AuthFlow: auth, begin: begin, await: await}, nil
}

func (f deviceAuthorizationAuthFlow) BeginAuthorization(ctx context.Context, input AuthInput) (AuthorizationTransition, error) {
	device, err := f.begin(ctx, input)
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("begin device authorization")
	}
	now := time.Now()
	if strings.TrimSpace(device.DeviceCode) == "" || len(device.DeviceCode) > 8192 || strings.TrimSpace(device.UserCode) == "" || len(device.UserCode) > 256 {
		return AuthorizationTransition{}, fmt.Errorf("device authorization response is incomplete")
	}
	base, err := validDeviceURL(device.VerificationURL)
	if err != nil {
		return AuthorizationTransition{}, err
	}
	displayURL := base
	if strings.TrimSpace(device.VerificationURLComplete) != "" {
		displayURL, err = validDeviceURL(device.VerificationURLComplete)
		if err != nil {
			return AuthorizationTransition{}, err
		}
	}
	if !device.ExpiresAt.After(now) || device.ExpiresAt.After(now.Add(MaxAuthorizationDuration)) {
		return AuthorizationTransition{}, fmt.Errorf("device authorization expiry must be in the future and within the allowed duration")
	}
	if device.PollInterval < 0 || device.PollInterval > time.Minute {
		return AuthorizationTransition{}, fmt.Errorf("device authorization interval must be 0..1m")
	}
	stateJSON, err := json.Marshal(deviceAuthorizationState{DeviceCode: device.DeviceCode, UserCode: device.UserCode, VerificationURL: device.VerificationURL, VerificationURLComplete: device.VerificationURLComplete, ExpiresAt: device.ExpiresAt, PollInterval: device.PollInterval, PrivateState: device.PrivateState})
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("encode device authorization state")
	}
	state, err := NewAuthorizationState(string(stateJSON))
	if err != nil {
		return AuthorizationTransition{}, err
	}
	return AuthorizationTransition{State: state, ExpiresAt: device.ExpiresAt, Await: true, Actions: []AuthorizationAction{
		{ID: "verification_code", Kind: AuthorizationDisplayCode, Label: "Verification code", Code: device.UserCode},
		{ID: "verification_url", Kind: AuthorizationOpenURL, Label: "Open verification page", URL: displayURL},
	}}, nil
}

func (f deviceAuthorizationAuthFlow) AdvanceAuthorization(ctx context.Context, state AuthorizationState, event AuthorizationEvent) (AuthorizationTransition, error) {
	if event.Kind != AuthorizationAwait {
		return AuthorizationTransition{}, fmt.Errorf("unsupported device authorization event")
	}
	var stored deviceAuthorizationState
	if err := json.Unmarshal([]byte(state.Encoded()), &stored); err != nil || strings.TrimSpace(stored.DeviceCode) == "" || stored.ExpiresAt.IsZero() {
		return AuthorizationTransition{}, fmt.Errorf("invalid device authorization state")
	}
	if !time.Now().Before(stored.ExpiresAt) {
		return AuthorizationTransition{Failure: AuthorizationExpired}, nil
	}
	device := DeviceAuthorization{DeviceCode: stored.DeviceCode, UserCode: stored.UserCode, VerificationURL: stored.VerificationURL, VerificationURLComplete: stored.VerificationURLComplete, ExpiresAt: stored.ExpiresAt, PollInterval: stored.PollInterval, PrivateState: stored.PrivateState}
	credential, err := f.await(ctx, device)
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) {
			switch retrieve.ErrorCode {
			case "access_denied":
				return AuthorizationTransition{Failure: AuthorizationDenied}, nil
			case "expired_token":
				return AuthorizationTransition{Failure: AuthorizationExpired}, nil
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return AuthorizationTransition{Failure: AuthorizationExpired}, nil
		}
		return AuthorizationTransition{}, fmt.Errorf("device authorization failed")
	}
	if err := credential.Validate(); err != nil {
		return AuthorizationTransition{}, fmt.Errorf("device authorization returned invalid credential state")
	}
	return AuthorizationTransition{Credential: &credential}, nil
}

func validDeviceURL(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 8192 {
		return "", fmt.Errorf("device authorization URL is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("device authorization URL must be an absolute HTTP/S URL")
	}
	return u.String(), nil
}
