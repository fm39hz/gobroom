package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestDeviceAuthorizationDriverKeepsSecretsOpaqueAndAwaitsOnDemand(t *testing.T) {
	called := 0
	expiry := time.Now().Add(5 * time.Minute)
	flow, err := NewDeviceAuthorizationDriver(OAuthAuth{}, func(context.Context, AuthInput) (DeviceAuthorization, error) {
		return DeviceAuthorization{DeviceCode: "private-device-code", UserCode: "ABCD-EFGH", VerificationURL: "https://login.example/verify", VerificationURLComplete: "https://login.example/verify?user=one", ExpiresAt: expiry, PollInterval: 7 * time.Second, PrivateState: json.RawMessage(`{"tenant":"internal"}`)}, nil
	}, func(_ context.Context, got DeviceAuthorization) (CredentialState, error) {
		called++
		if got.DeviceCode != "private-device-code" || got.UserCode != "ABCD-EFGH" || got.VerificationURL != "https://login.example/verify" || got.VerificationURLComplete != "https://login.example/verify?user=one" || got.PollInterval != 7*time.Second || string(got.PrivateState) != `{"tenant":"internal"}` {
			t.Fatalf("await received incomplete state: %#v", got)
		}
		return NewCredentialState(`{"access_token":"secret"}`)
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := flow.(AuthorizationFlow)
	transition, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !transition.Await || transition.ExpiresAt != expiry || len(transition.Actions) != 2 || transition.Actions[0].Code != "ABCD-EFGH" || transition.Actions[1].URL != "https://login.example/verify?user=one" {
		t.Fatalf("unexpected begin transition: %#v", transition)
	}
	public, err := json.Marshal(transition)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "private-device-code") || !strings.Contains(string(public), "ABCD-EFGH") {
		t.Fatal("public action projection exposed private code or lost user instructions")
	}
	if called != 0 {
		t.Fatal("device authorization was polled before an await event")
	}
	completed, err := authorizer.AdvanceAuthorization(context.Background(), transition.State, AuthorizationEvent{Kind: AuthorizationAwait})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || completed.Credential == nil || completed.Credential.Encoded() != `{"access_token":"secret"}` {
		t.Fatalf("unexpected completed transition: %#v", completed)
	}
}

func TestDeviceAuthorizationDriverMapsKnownFailuresAndSanitizesErrors(t *testing.T) {
	for _, test := range []struct {
		code string
		want AuthorizationFailure
	}{
		{"access_denied", AuthorizationDenied}, {"expired_token", AuthorizationExpired},
	} {
		flow, err := NewDeviceAuthorizationDriver(OAuthAuth{}, func(context.Context, AuthInput) (DeviceAuthorization, error) {
			return DeviceAuthorization{DeviceCode: "secret-code", UserCode: "USER", VerificationURL: "https://login.example", ExpiresAt: time.Now().Add(time.Minute)}, nil
		}, func(context.Context, DeviceAuthorization) (CredentialState, error) {
			return CredentialState{}, &oauth2.RetrieveError{ErrorCode: test.code}
		})
		if err != nil {
			t.Fatal(err)
		}
		started, err := flow.(AuthorizationFlow).BeginAuthorization(context.Background(), AuthInput{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := flow.(AuthorizationFlow).AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{Kind: AuthorizationAwait})
		if err != nil || result.Failure != test.want {
			t.Fatalf("failure=%q err=%v", result.Failure, err)
		}
	}
	flow, err := NewDeviceAuthorizationDriver(OAuthAuth{}, func(context.Context, AuthInput) (DeviceAuthorization, error) {
		return DeviceAuthorization{DeviceCode: "secret-code", UserCode: "USER", VerificationURL: "https://login.example", ExpiresAt: time.Now().Add(time.Minute)}, nil
	}, func(context.Context, DeviceAuthorization) (CredentialState, error) {
		return CredentialState{}, errors.New("response included secret payload")
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := flow.(AuthorizationFlow).BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.(AuthorizationFlow).AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{Kind: AuthorizationAwait})
	if err == nil || strings.Contains(err.Error(), "secret payload") {
		t.Fatalf("provider error was not sanitized: %v", err)
	}
}
