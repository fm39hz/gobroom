package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthSetupModeChoicePreservesCodeAndDevice(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_code":"private-device","user_code":"PUBLIC-CODE","verification_uri":"https://identity.test/verify","expires_in":600,"interval":1}`)
	}))
	defer issuer.Close()
	opts := AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "client", AuthURL: "https://identity.test/authorize", TokenURL: issuer.URL + "/token", DeviceAuthURL: issuer.URL + "/device", RedirectURL: "http://127.0.0.1:0/callback"}}
	for _, method := range []OAuthSetupMode{OAuthSetupCode, OAuthSetupDevice} {
		t.Run(string(method), func(t *testing.T) {
			flow, err := OAuthAuthFactory(opts)
			if err != nil {
				t.Fatal(err)
			}
			authorizer := flow.(AuthorizationFlow)
			choice, err := authorizer.BeginAuthorization(context.Background(), AuthInput{})
			if err != nil || len(choice.Actions) != 1 || choice.Actions[0].ID != "select_method" || choice.PrepareCallbacks {
				t.Fatalf("dual-mode factory did not offer selection: %+v %v", choice, err)
			}
			value, err := json.Marshal(struct {
				Method OAuthSetupMode `json:"method"`
			}{method})
			if err != nil {
				t.Fatal(err)
			}
			next, err := authorizer.AdvanceAuthorization(context.Background(), choice.State, AuthorizationEvent{Kind: AuthorizationSubmit, ActionID: "select_method", Value: value})
			if err != nil {
				t.Fatal(err)
			}
			if method == OAuthSetupDevice {
				if !next.Await || len(next.Actions) != 2 {
					t.Fatal("device method was lost by choice strategy")
				}
			} else {
				if !next.PrepareCallbacks || len(next.Callbacks) != 1 {
					t.Fatal("code method was lost by choice strategy")
				}
				ready, err := authorizer.AdvanceAuthorization(context.Background(), next.State, AuthorizationEvent{Kind: AuthorizationCallbacksReady, Callbacks: []AuthorizationCallbackResource{{ActionID: "callback", URL: "http://127.0.0.1:43210/callback", Mode: AuthorizationCallbackLoopback}}})
				if err != nil || ready.PrepareCallbacks || len(ready.Actions) != 2 {
					t.Fatalf("nested code method failed resource handoff: %+v %v", ready, err)
				}
			}
		})
	}
	for _, mode := range []OAuthSetupMode{OAuthSetupCode, OAuthSetupDevice} {
		configured := *opts.OAuth
		configured.SetupMode = mode
		flow, err := OAuthAuthFactory(AuthOptions{OAuth: &configured})
		if err != nil {
			t.Fatal(err)
		}
		start, err := flow.(AuthorizationFlow).BeginAuthorization(context.Background(), AuthInput{})
		if err != nil {
			t.Fatal(err)
		}
		if len(start.Actions) == 1 && start.Actions[0].ID == "select_method" {
			t.Fatal("explicit mode still required a selection roundtrip")
		}
	}
}

func TestOAuthSetupInputSchemasAreRegisteredAndStrict(t *testing.T) {
	runtime, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{`{"url":"https://client.test/callback?state=x&code=y"}`, `{"code":"code","state":"nonce"}`} {
		if err := snapshot.ValidateSchema(AuthorizationCodeInputSchemaRef, json.RawMessage(valid)); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{`{}`, `{"url":"x","code":"c","state":"s"}`, `{"code":"c"}`, `{"code":"c","state":"s","extra":true}`} {
		if err := snapshot.ValidateSchema(AuthorizationCodeInputSchemaRef, json.RawMessage(invalid)); err == nil {
			t.Fatal("invalid code input satisfied schema")
		}
	}
	if err := snapshot.ValidateSchema(oauthMethodInputSchemaRef, json.RawMessage(`{"method":"other"}`)); err == nil {
		t.Fatal("unknown method satisfied schema")
	}
	if _, err := OAuthAuthFactory(AuthOptions{OAuth: &OAuthFlowOptions{ClientID: "client", AuthURL: "https://issuer.test/auth", TokenURL: "https://issuer.test/token", SetupMode: "other"}}); err == nil {
		t.Fatal("unknown configured setup mode accepted")
	}
}
