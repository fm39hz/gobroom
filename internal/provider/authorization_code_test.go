package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	appauth "github.com/fm39hz/gobroom/internal/auth"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

const codeRedirect = "http://127.0.0.1:43127/oauth/callback?tenant=blue"

type codeFlowFixture struct{}

func (codeFlowFixture) ID() string               { return "code-fixture" }
func (codeFlowFixture) SetupSchema() SetupSchema { return SetupSchema{} }
func (codeFlowFixture) Resolve(context.Context, AuthInput) (credential kernel.Credential, err error) {
	return
}
func (codeFlowFixture) Refresh(context.Context, AuthInput, kernel.Credential) (CredentialState, error) {
	return CredentialState{}, errors.New("not used")
}

func newCodeTestDriver(t *testing.T, redirect string, callbackRequired bool, authorize func(context.Context, AuthorizationCodeRequest) (string, error), exchange func(context.Context, AuthorizationCodeExchange) (CredentialState, error)) AuthorizationFlow {
	t.Helper()
	flow, err := NewAuthorizationCodeDriver(codeFlowFixture{}, AuthorizationCodeOptions{
		RedirectURL: redirect, CallbackRequired: callbackRequired,
	}, authorize, exchange)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, ok := flow.(AuthorizationFlow)
	if !ok {
		t.Fatalf("code driver does not implement generic authorization: %T", flow)
	}
	return authorizer
}

func TestAuthorizationCodeFixedLoopbackPreparesBeforeConsent(t *testing.T) {
	consentCalls := 0
	authorizer := newCodeTestDriver(t, codeRedirect, true, func(context.Context, AuthorizationCodeRequest) (string, error) {
		consentCalls++
		return "https://identity.example/authorize", nil
	}, func(context.Context, AuthorizationCodeExchange) (CredentialState, error) {
		return NewCredentialState("opaque-state")
	})
	started, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !started.PrepareCallbacks || len(started.Callbacks) != 1 || started.Callbacks[0].URL != codeRedirect || consentCalls != 0 {
		t.Fatalf("fixed loopback did not prepare before consent: transition=%#v consentCalls=%d", started, consentCalls)
	}
	ready, err := authorizer.AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{
		Kind: AuthorizationCallbacksReady, Callbacks: []AuthorizationCallbackResource{{ActionID: "callback", URL: codeRedirect, Mode: AuthorizationCallbackLoopback}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.PrepareCallbacks || len(ready.Callbacks) != 1 || consentCalls != 1 || ready.Callbacks[0].URL != codeRedirect {
		t.Fatalf("callbacks-ready transition changed the fixed target or skipped consent: %#v calls=%d", ready, consentCalls)
	}
	if ready.State.Encoded() == started.State.Encoded() {
		t.Fatal("resolved callback state was not carried forward")
	}
}

func TestAuthorizationCodeAllocatedLoopbackBindsResolvedURIBeforeConsent(t *testing.T) {
	var consentRequest AuthorizationCodeRequest
	authorizer := newCodeTestDriver(t, "http://127.0.0.1:0/oauth/callback?tenant=blue", true, func(_ context.Context, request AuthorizationCodeRequest) (string, error) {
		consentRequest = request
		return "https://identity.example/authorize", nil
	}, func(context.Context, AuthorizationCodeExchange) (CredentialState, error) {
		return NewCredentialState("opaque-state")
	})
	started, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	resolved := "http://127.0.0.1:45321/oauth/callback?tenant=blue"
	ready, err := authorizer.AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{
		Kind: AuthorizationCallbacksReady, Callbacks: []AuthorizationCallbackResource{{ActionID: "callback", URL: resolved, Mode: AuthorizationCallbackLoopback}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if consentRequest.RedirectURL != resolved || ready.Callbacks[0].URL != resolved {
		t.Fatalf("consent/callback did not use resolved URI: request=%#v callbacks=%#v", consentRequest, ready.Callbacks)
	}
}

func TestAuthorizationCodeRemoteHTTPSBeginsConsentWithoutCallbackGrant(t *testing.T) {
	called := false
	authorizer := newCodeTestDriver(t, "https://client.example/oauth/return", true, func(_ context.Context, request AuthorizationCodeRequest) (string, error) {
		called = true
		if request.RedirectURL != "https://client.example/oauth/return" {
			t.Fatalf("redirect URI=%q", request.RedirectURL)
		}
		return "https://identity.example/authorize", nil
	}, func(context.Context, AuthorizationCodeExchange) (CredentialState, error) {
		return NewCredentialState("opaque-state")
	})
	started, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !called || started.PrepareCallbacks || len(started.Callbacks) != 0 || len(started.Actions) != 2 || started.Actions[0].Kind != AuthorizationOpenURL {
		t.Fatalf("remote HTTPS flow unexpectedly required a callback grant: called=%v transition=%#v", called, started)
	}
}

func TestAuthorizationCodeInputValidationNeverExchangesBadInput(t *testing.T) {
	authorizeCalls, exchangeCalls := 0, 0
	authorizer := newCodeTestDriver(t, codeRedirect, false, func(context.Context, AuthorizationCodeRequest) (string, error) {
		authorizeCalls++
		return "https://identity.example/authorize", nil
	}, func(context.Context, AuthorizationCodeExchange) (CredentialState, error) {
		exchangeCalls++
		return NewCredentialState("opaque-state")
	})
	started, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	started, err = authorizer.AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{Kind: AuthorizationCallbacksReady, Callbacks: []AuthorizationCallbackResource{{ActionID: "callback", URL: codeRedirect, Mode: AuthorizationCallbackLoopback}}})
	if err != nil {
		t.Fatal(err)
	}
	private := decodeCodePrivate(t, started.State)
	validURL := "http://127.0.0.1:43127/oauth/callback?tenant=blue&code=auth-code&state=" + url.QueryEscape(private.Nonce)
	cases := []struct {
		name  string
		input string
	}{
		{"wrong host", `{"url":"` + strings.Replace(validURL, "127.0.0.1", "127.0.0.2", 1) + `"}`},
		{"wrong path", `{"url":"` + strings.Replace(validURL, "/oauth/callback", "/elsewhere", 1) + `"}`},
		{"missing fixed query", `{"url":"http://127.0.0.1:43127/oauth/callback?code=auth-code&state=` + url.QueryEscape(private.Nonce) + `"}`},
		{"duplicate fixed query", `{"url":"http://127.0.0.1:43127/oauth/callback?tenant=blue&tenant=blue&code=auth-code&state=` + url.QueryEscape(private.Nonce) + `"}`},
		{"duplicate code", `{"url":"http://127.0.0.1:43127/oauth/callback?tenant=blue&code=one&code=two&state=` + url.QueryEscape(private.Nonce) + `"}`},
		{"unknown field", `{"url":"` + validURL + `","extra":"x"}`},
		{"unknown callback parameter", `{"url":"` + validURL + `&unexpected=x"}`},
		{"state mismatch", `{"url":"http://127.0.0.1:43127/oauth/callback?tenant=blue&code=auth-code&state=wrong"}`},
		{"provider error", `{"url":"http://127.0.0.1:43127/oauth/callback?tenant=blue&error=access_denied&state=` + url.QueryEscape(private.Nonce) + `"}`},
		{"empty code", `{"url":"http://127.0.0.1:43127/oauth/callback?tenant=blue&state=` + url.QueryEscape(private.Nonce) + `"}`},
		{"oversized code", `{"code":"` + strings.Repeat("c", 4097) + `","state":"` + private.Nonce + `"}`},
		{"oversized state", `{"code":"x","state":"` + strings.Repeat("s", 257) + `"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := exchangeCalls
			result, err := authorizer.AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{Kind: AuthorizationSubmit, ActionID: "callback", Value: json.RawMessage(test.input)})
			if test.name == "provider error" {
				if err != nil || result.Failure != AuthorizationDenied {
					t.Fatalf("denial result=%#v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid callback input was accepted")
			}
			if exchangeCalls != before {
				t.Fatal("invalid callback input reached token exchange")
			}
		})
	}
	if authorizeCalls != 1 || exchangeCalls != 0 {
		t.Fatalf("unexpected handler calls: authorize=%d exchange=%d", authorizeCalls, exchangeCalls)
	}
}

func TestAuthorizationCodeInputAcceptsCodeAndStateAndExchangesExactPKCEValues(t *testing.T) {
	var request AuthorizationCodeRequest
	var exchange AuthorizationCodeExchange
	authorizer := newCodeTestDriver(t, codeRedirect, false, func(_ context.Context, got AuthorizationCodeRequest) (string, error) {
		request = got
		return "https://identity.example/authorize", nil
	}, func(_ context.Context, got AuthorizationCodeExchange) (CredentialState, error) {
		exchange = got
		return NewCredentialState(`{"vendor_token":"opaque","refresh_blob":{"keep":true}}`)
	})
	started, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	started, err = authorizer.AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{Kind: AuthorizationCallbacksReady, Callbacks: []AuthorizationCallbackResource{{ActionID: "callback", URL: codeRedirect, Mode: AuthorizationCallbackLoopback}}})
	if err != nil {
		t.Fatal(err)
	}
	private := decodeCodePrivate(t, started.State)
	if len(request.Verifier) != 43 || request.State != private.Nonce || request.RedirectURL != codeRedirect {
		t.Fatalf("authorization request did not preserve generated secrets/redirect: state=%q verifier length=%d redirect=%q", request.State, len(request.Verifier), request.RedirectURL)
	}
	for name, value := range map[string]string{"state nonce": request.State, "PKCE verifier": request.Verifier} {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(raw) != 32 {
			t.Fatalf("%s does not have GenerateVerifier format: length=%d err=%v", name, len(raw), err)
		}
	}
	input, _ := json.Marshal(AuthorizationCodeInput{Code: "submitted-code", State: private.Nonce})
	result, err := authorizer.AdvanceAuthorization(context.Background(), started.State, AuthorizationEvent{Kind: AuthorizationSubmit, ActionID: "callback", Value: input})
	if err != nil {
		t.Fatal(err)
	}
	wantState := `{"vendor_token":"opaque","refresh_blob":{"keep":true}}`
	if exchange.Code != "submitted-code" || exchange.Verifier != private.Verifier || exchange.RedirectURL != codeRedirect || result.Credential == nil || result.Credential.Encoded() != wantState {
		t.Fatalf("exchange/result lost opaque values: exchange=%#v credential=%#v", exchange, result.Credential)
	}
}

func TestOAuthAuthorizationCodeHookUsesPKCES256AndGeneratedNonce(t *testing.T) {
	verifier := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	nonce := "generated-state-nonce"
	auth := OAuthAuth{Config: appauth.OAuthConfig{ClientID: "client", AuthURL: "https://identity.example/authorize", TokenURL: "https://identity.example/token", RedirectURL: "https://client.example/callback"}}
	consent, err := auth.authorizationCodeURL(context.Background(), AuthorizationCodeRequest{State: nonce, Verifier: verifier, RedirectURL: auth.Config.RedirectURL})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(consent)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(verifier))
	if parsed.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) || parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("state") != nonce {
		t.Fatalf("OAuth authorize URL omitted S256/state: %s", consent)
	}
}

func TestAuthorizationCodeSecretsAreRedactedAndInputSchemaRefIsTyped(t *testing.T) {
	state := AuthorizationState{encoded: `{"nonce":"private-nonce","verifier":"private-verifier"}`}
	request := AuthorizationCodeRequest{State: "private-state", Verifier: "private-verifier", RedirectURL: codeRedirect}
	exchange := AuthorizationCodeExchange{Code: "private-code", Verifier: "private-verifier", RedirectURL: codeRedirect}
	for _, value := range []any{state, request, exchange} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-nonce", "private-state", "private-verifier", "private-code", "43127", "tenant"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("JSON representation leaked %q: %s", secret, encoded)
			}
		}
	}
	if AuthorizationCodeInputSchemaRef.Kind != extensions.SchemaKind || AuthorizationCodeInputSchemaRef.ID == "" || AuthorizationCodeInputSchemaRef.ContractVersion == 0 {
		t.Fatalf("invalid built-in authorization input schema ref: %#v", AuthorizationCodeInputSchemaRef)
	}
}

func TestAuthorizationCodeBeginAndAdvanceHonorCancellation(t *testing.T) {
	called := 0
	authorizer := newCodeTestDriver(t, "https://client.example/callback", false, func(context.Context, AuthorizationCodeRequest) (string, error) {
		called++
		return "https://identity.example/authorize", nil
	}, func(context.Context, AuthorizationCodeExchange) (CredentialState, error) {
		called++
		return NewCredentialState("credential")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authorizer.BeginAuthorization(ctx, AuthInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled begin error=%v", err)
	}
	started, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
	if err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := authorizer.AdvanceAuthorization(deadline, started.State, AuthorizationEvent{Kind: AuthorizationSubmit, ActionID: "callback", Value: json.RawMessage(`{"code":"x","state":"y"}`)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired advance error=%v", err)
	}
	if called != 1 {
		t.Fatalf("canceled paths invoked handlers unexpectedly: %d", called)
	}
}

func decodeCodePrivate(t *testing.T, state AuthorizationState) authorizationCodePrivate {
	t.Helper()
	var private authorizationCodePrivate
	if err := json.Unmarshal([]byte(state.Encoded()), &private); err != nil {
		t.Fatal(err)
	}
	return private
}
