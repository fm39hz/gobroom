package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func validCallback() AuthorizationCallback {
	state, _ := NewAuthorizationState(strings.Repeat("s", 32))
	return AuthorizationCallback{
		ActionID: "submit", URL: "http://127.0.0.1:43123/callback?fixed=one",
		CorrelationParameter: "state", CorrelationValue: state,
		ResponseParameters: []string{"code"}, Required: true,
	}
}

func TestAuthorizationCallbackRedactsAllRepresentations(t *testing.T) {
	callback := validCallback()
	callback.URL = "http://127.0.0.1:43123/callback?fixed=private-query-value"
	callback.CorrelationValue, _ = NewAuthorizationState(strings.Repeat("private-correlation", 2))
	for _, rendered := range []string{callback.String(), fmt.Sprintf("%#v", callback)} {
		if strings.Contains(rendered, "private") || strings.Contains(rendered, "127.0.0.1") {
			t.Fatalf("callback leaked in %q", rendered)
		}
	}
	data, err := json.Marshal(callback)
	if err != nil || string(data) != `"[redacted]"` {
		t.Fatalf("MarshalJSON = %s, %v", data, err)
	}
	data, err = json.Marshal(AuthorizationTransition{Callbacks: []AuthorizationCallback{callback}})
	if err != nil || strings.Contains(string(data), "private") || strings.Contains(string(data), "127.0.0.1") {
		t.Fatalf("transition serialization leaked callback: %s, %v", data, err)
	}
}

func TestAuthorizationCallbackValidationBoundsAndURLs(t *testing.T) {
	valid := validCallback()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid callback: %v", err)
	}
	for name, mutate := range map[string]func(*AuthorizationCallback){
		"HTTPS requires external TLS": func(c *AuthorizationCallback) { c.URL = "https://127.0.0.1:1234/callback" },
		"nonloopback":                 func(c *AuthorizationCallback) { c.URL = "http://example.invalid:1234/callback" },
		"missing port":                func(c *AuthorizationCallback) { c.URL = "http://localhost/callback" },
		"zero port":                   func(c *AuthorizationCallback) { c.URL = "http://localhost:0/callback" },
		"missing path":                func(c *AuthorizationCallback) { c.URL = "http://localhost:1234" },
		"userinfo":                    func(c *AuthorizationCallback) { c.URL = "http://user@localhost:1234/callback" },
		"fragment":                    func(c *AuthorizationCallback) { c.URL = "http://localhost:1234/callback#secret" },
		"duplicate query key":         func(c *AuthorizationCallback) { c.URL = "http://localhost:1234/callback?fixed=one&fixed=two" },
		"correlation in query":        func(c *AuthorizationCallback) { c.URL = "http://localhost:1234/callback?state=one" },
		"response collision":          func(c *AuthorizationCallback) { c.ResponseParameters = []string{"fixed"} },
		"duplicate response":          func(c *AuthorizationCallback) { c.ResponseParameters = []string{"code", "code"} },
		"oversized URL":               func(c *AuthorizationCallback) { c.URL = "http://localhost:1234/" + strings.Repeat("x", 8192) },
		"oversized correlation": func(c *AuthorizationCallback) {
			c.CorrelationValue, _ = NewAuthorizationState(strings.Repeat("x", 257))
		},
	} {
		callback := valid
		mutate(&callback)
		if err := callback.Validate(); err == nil {
			t.Errorf("%s unexpectedly validated", name)
		}
	}
}

func TestAuthorizationTransitionCallbackRequiresContinuingInputAction(t *testing.T) {
	now := time.Now()
	state, _ := NewAuthorizationState("transition-state")
	base := AuthorizationTransition{
		State: state, ExpiresAt: now.Add(time.Minute),
		Actions:   []AuthorizationAction{{ID: "submit", Kind: AuthorizationInput, Label: "Submit", InputSchemaRef: &extensions.Ref{Kind: extensions.SchemaKind, ID: "callback-form", ContractVersion: 1}}},
		Callbacks: []AuthorizationCallback{validCallback()},
	}
	if err := base.Validate(now); err != nil {
		t.Fatalf("valid callback transition: %v", err)
	}
	withoutAction := base
	withoutAction.Actions = nil
	if err := withoutAction.Validate(now); err == nil {
		t.Fatal("accepted callback without matching offered action")
	}
	terminal, _ := NewCredentialState("credential")
	completed := AuthorizationTransition{Credential: &terminal, Callbacks: []AuthorizationCallback{validCallback()}}
	if err := completed.Validate(now); err == nil {
		t.Fatal("accepted callbacks on terminal credential transition")
	}
	failed := AuthorizationTransition{Failure: AuthorizationDenied, Callbacks: []AuthorizationCallback{validCallback()}}
	if err := failed.Validate(now); err == nil {
		t.Fatal("accepted callbacks on terminal failure transition")
	}
}
