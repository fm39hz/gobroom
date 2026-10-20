package provider

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"golang.org/x/oauth2"
)

var AuthorizationCodeInputSchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.auth.authorization-code.input", ContractVersion: 1}

type AuthorizationCodeOptions struct {
	RedirectURL      string
	InputSchemaRef   extensions.Ref
	TTL              time.Duration
	CallbackRequired bool
}

type AuthorizationCodeRequest struct{ State, Verifier, RedirectURL string }

func (AuthorizationCodeRequest) String() string { return "[redacted]" }
func (AuthorizationCodeRequest) GoString() string {
	return "provider.AuthorizationCodeRequest{[redacted]}"
}
func (AuthorizationCodeRequest) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

type AuthorizationCodeExchange struct{ Code, Verifier, RedirectURL string }

func (AuthorizationCodeExchange) String() string { return "[redacted]" }
func (AuthorizationCodeExchange) GoString() string {
	return "provider.AuthorizationCodeExchange{[redacted]}"
}
func (AuthorizationCodeExchange) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

type AuthorizationCodeInput struct {
	URL   string `json:"url,omitempty"`
	Code  string `json:"code,omitempty"`
	State string `json:"state,omitempty"`
}

type authorizationCodePrivate struct {
	Nonce       string    `json:"nonce"`
	Verifier    string    `json:"verifier"`
	RedirectURL string    `json:"redirectUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Preparing   bool      `json:"preparing"`
	Local       bool      `json:"local"`
}

type authorizationCodeDriver struct {
	AuthFlow
	options   AuthorizationCodeOptions
	authorize func(context.Context, AuthorizationCodeRequest) (string, error)
	exchange  func(context.Context, AuthorizationCodeExchange) (CredentialState, error)
}

func NewAuthorizationCodeDriver(auth AuthFlow, options AuthorizationCodeOptions, authorize func(context.Context, AuthorizationCodeRequest) (string, error), exchange func(context.Context, AuthorizationCodeExchange) (CredentialState, error)) (AuthFlow, error) {
	if auth == nil || authorize == nil || exchange == nil {
		return nil, fmt.Errorf("authorization code requires auth flow and handlers")
	}
	target, err := url.Parse(options.RedirectURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.Fragment != "" || target.Path == "" {
		return nil, fmt.Errorf("authorization redirect target is invalid")
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("authorization redirect query is invalid")
	}
	for key, values := range query {
		if len(values) != 1 || oauthCodeResponseParameter(key) {
			return nil, fmt.Errorf("authorization redirect query conflicts with the response")
		}
	}
	if options.InputSchemaRef == (extensions.Ref{}) {
		options.InputSchemaRef = AuthorizationCodeInputSchemaRef
	}
	if options.InputSchemaRef.Kind != extensions.SchemaKind || options.InputSchemaRef.Validate() != nil {
		return nil, fmt.Errorf("authorization code input schema reference is invalid")
	}
	if options.TTL == 0 {
		options.TTL = 10 * time.Minute
	}
	if options.TTL <= 0 || options.TTL > MaxAuthorizationDuration {
		return nil, fmt.Errorf("authorization code TTL is invalid")
	}
	return authorizationCodeDriver{AuthFlow: auth, options: options, authorize: authorize, exchange: exchange}, nil
}

func (f authorizationCodeDriver) callback(p authorizationCodePrivate) AuthorizationCallback {
	correlation, _ := NewAuthorizationState(p.Nonce)
	return AuthorizationCallback{ActionID: "callback", URL: p.RedirectURL, CorrelationParameter: "state", CorrelationValue: correlation, ResponseParameters: []string{"code", "error", "error_description", "error_uri"}, Required: f.options.CallbackRequired}
}

func (f authorizationCodeDriver) BeginAuthorization(ctx context.Context, _ AuthInput) (AuthorizationTransition, error) {
	if err := ctx.Err(); err != nil {
		return AuthorizationTransition{}, err
	}
	p := authorizationCodePrivate{Nonce: oauth2.GenerateVerifier(), Verifier: oauth2.GenerateVerifier(), RedirectURL: f.options.RedirectURL, ExpiresAt: time.Now().Add(f.options.TTL)}
	target, _ := url.Parse(p.RedirectURL)
	p.Local = target.Scheme == "http" && (target.Hostname() == "localhost" || (net.ParseIP(target.Hostname()) != nil && net.ParseIP(target.Hostname()).IsLoopback()))
	if p.Local {
		if f.callback(p).Validate() != nil {
			return AuthorizationTransition{}, fmt.Errorf("authorization callback grant is invalid")
		}
		p.Preparing = true
		return f.transition(ctx, p)
	}
	if target.Port() == "0" {
		return AuthorizationTransition{}, fmt.Errorf("remote authorization target cannot request allocation")
	}
	return f.transition(ctx, p)
}

func (f authorizationCodeDriver) transition(ctx context.Context, p authorizationCodePrivate) (AuthorizationTransition, error) {
	encoded, err := json.Marshal(p)
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("encode authorization state")
	}
	state, err := NewAuthorizationState(string(encoded))
	if err != nil {
		return AuthorizationTransition{}, err
	}
	transition := AuthorizationTransition{State: state, ExpiresAt: p.ExpiresAt, Actions: []AuthorizationAction{{ID: "callback", Kind: AuthorizationInput, Label: "Submit callback URL or code and state", InputSchemaRef: &f.options.InputSchemaRef}}}
	if p.Local {
		transition.Callbacks = []AuthorizationCallback{f.callback(p)}
	}
	if p.Preparing {
		transition.PrepareCallbacks = true
		return transition, nil
	}
	if err := ctx.Err(); err != nil {
		return AuthorizationTransition{}, err
	}
	consent, err := f.authorize(ctx, AuthorizationCodeRequest{State: p.Nonce, Verifier: p.Verifier, RedirectURL: p.RedirectURL})
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("build authorization consent")
	}
	parsed, err := url.Parse(consent)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" || len(consent) > 8192 {
		return AuthorizationTransition{}, fmt.Errorf("authorization consent URL is invalid")
	}
	transition.Actions = append([]AuthorizationAction{{ID: "consent", Kind: AuthorizationOpenURL, Label: "Approve access", URL: consent}}, transition.Actions...)
	return transition, nil
}

func (f authorizationCodeDriver) AdvanceAuthorization(ctx context.Context, state AuthorizationState, event AuthorizationEvent) (AuthorizationTransition, error) {
	if err := ctx.Err(); err != nil {
		return AuthorizationTransition{}, err
	}
	var p authorizationCodePrivate
	if json.Unmarshal([]byte(state.Encoded()), &p) != nil || len(p.Nonce) < 32 || p.Verifier == "" || p.RedirectURL == "" {
		return AuthorizationTransition{}, fmt.Errorf("invalid private authorization state")
	}
	if !p.ExpiresAt.After(time.Now()) {
		return AuthorizationTransition{Failure: AuthorizationExpired}, nil
	}
	if event.Kind == AuthorizationCallbacksReady {
		if !p.Preparing || len(event.Callbacks) != 1 || event.Callbacks[0].ActionID != "callback" {
			return AuthorizationTransition{}, fmt.Errorf("unexpected callback resource")
		}
		resource := event.Callbacks[0]
		requested, _ := url.Parse(p.RedirectURL)
		resolved, err := url.Parse(resource.URL)
		if err != nil || requested.Scheme != resolved.Scheme || requested.Hostname() != resolved.Hostname() || requested.EscapedPath() != resolved.EscapedPath() || requested.RawQuery != resolved.RawQuery || resolved.Port() == "" || resolved.Port() == "0" || (requested.Port() != "0" && requested.Host != resolved.Host) || (resource.Mode != AuthorizationCallbackLoopback && resource.Mode != AuthorizationCallbackManual) {
			return AuthorizationTransition{}, fmt.Errorf("callback resource does not match authorization intent")
		}
		p.RedirectURL, p.Preparing = resource.URL, false
		return f.transition(ctx, p)
	}
	if p.Preparing || event.Kind != AuthorizationSubmit || event.ActionID != "callback" {
		return AuthorizationTransition{}, fmt.Errorf("unsupported authorization event")
	}
	var input AuthorizationCodeInput
	decoder := json.NewDecoder(bytes.NewReader(event.Value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return AuthorizationTransition{}, AuthorizationInputRejected{}
	}
	code, stateNonce := input.Code, input.State
	if input.URL != "" {
		if input.Code != "" || input.State != "" || len(input.URL) > 8192 {
			return AuthorizationTransition{}, AuthorizationInputRejected{}
		}
		actual, err := url.Parse(input.URL)
		if err != nil || actual.User != nil || actual.Fragment != "" {
			return AuthorizationTransition{}, AuthorizationInputRejected{}
		}
		expected, _ := url.Parse(p.RedirectURL)
		query, err := url.ParseQuery(actual.RawQuery)
		if err != nil || !sameAuthorizationCodeTarget(expected, actual, query) {
			return AuthorizationTransition{}, AuthorizationInputRejected{}
		}
		stateNonce = query.Get("state")
		if subtle.ConstantTimeCompare([]byte(stateNonce), []byte(p.Nonce)) != 1 {
			return AuthorizationTransition{}, AuthorizationInputRejected{}
		}
		if providerError := query.Get("error"); providerError != "" {
			switch providerError {
			case "access_denied":
				return AuthorizationTransition{Failure: AuthorizationDenied}, nil
			case "expired_token":
				return AuthorizationTransition{Failure: AuthorizationExpired}, nil
			default:
				return AuthorizationTransition{Failure: AuthorizationUnavailable}, nil
			}
		}
		code = query.Get("code")
	}
	if code == "" || len(code) > 4096 || len(stateNonce) > 256 || subtle.ConstantTimeCompare([]byte(stateNonce), []byte(p.Nonce)) != 1 {
		return AuthorizationTransition{}, AuthorizationInputRejected{}
	}
	credential, err := f.exchange(ctx, AuthorizationCodeExchange{Code: code, Verifier: p.Verifier, RedirectURL: p.RedirectURL})
	if err != nil {
		return AuthorizationTransition{}, fmt.Errorf("authorization exchange failed")
	}
	if credential.Validate() != nil {
		return AuthorizationTransition{}, fmt.Errorf("authorization exchange returned invalid credential state")
	}
	return AuthorizationTransition{Credential: &credential}, nil
}

func oauthCodeResponseParameter(key string) bool {
	switch key {
	case "code", "state", "error", "error_description", "error_uri":
		return true
	default:
		return false
	}
}

func sameAuthorizationCodeTarget(expected, actual *url.URL, query url.Values) bool {
	if expected.Scheme != actual.Scheme || expected.Host != actual.Host || expected.EscapedPath() != actual.EscapedPath() {
		return false
	}
	fixed, err := url.ParseQuery(expected.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range fixed {
		if len(query[key]) != 1 || query[key][0] != values[0] {
			return false
		}
	}
	for key, values := range query {
		if len(values) != 1 || len(values[0]) > 4096 {
			return false
		}
		if _, exists := fixed[key]; !exists && !oauthCodeResponseParameter(key) {
			return false
		}
	}
	return true
}
