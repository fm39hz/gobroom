package externalprovider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
	"golang.org/x/oauth2"
)

const (
	codeProviderID = "example-external-code"
	codeAuthID     = "example-external-code-auth"
)

type codeIssuer struct {
	sync.Mutex
	baseURL                    string
	redirect, state, challenge string
	registrations, exchanges   int
	preConsentStatus           int
}

type codeModule struct{ issuer *codeIssuer }

func (codeModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.external-code", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}
func (m codeModule) Register(runtime *provider.RuntimeRegistry) error {
	return runtime.RegisterAuthFactory(codeAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return provider.NewAuthorizationCodeDriver(codeFlow{}, provider.AuthorizationCodeOptions{RedirectURL: "http://127.0.0.1:0/return?tenant=code-fixture"},
			func(ctx context.Context, request provider.AuthorizationCodeRequest) (string, error) {
				challenge := oauth2.S256ChallengeFromVerifier(request.Verifier)
				body, _ := json.Marshal(struct{ State, RedirectURL, Challenge string }{request.State, request.RedirectURL, challenge})
				registration, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer.baseURL+"/register", strings.NewReader(string(body)))
				if err != nil {
					return "", err
				}
				registration.Header.Set("Content-Type", "application/json")
				response, err := http.DefaultClient.Do(registration)
				if err != nil {
					return "", err
				}
				defer response.Body.Close()
				var result struct {
					Consent string `json:"consent"`
				}
				if response.StatusCode != http.StatusOK {
					return "", fmt.Errorf("issuer registration status %d", response.StatusCode)
				}
				if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
					return "", err
				}
				return result.Consent, nil
			},
			func(ctx context.Context, exchange provider.AuthorizationCodeExchange) (provider.CredentialState, error) {
				body, _ := json.Marshal(struct{ Code, Verifier, RedirectURL string }{exchange.Code, exchange.Verifier, exchange.RedirectURL})
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.issuer.baseURL+"/token", strings.NewReader(string(body)))
				if err != nil {
					return provider.CredentialState{}, err
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					return provider.CredentialState{}, err
				}
				defer response.Body.Close()
				var result struct {
					Credential string `json:"credential"`
				}
				if response.StatusCode != http.StatusOK {
					return provider.CredentialState{}, fmt.Errorf("issuer token status %d", response.StatusCode)
				}
				if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
					return provider.CredentialState{}, err
				}
				return provider.NewCredentialState(result.Credential)
			})
	})
}

type codeFlow struct{}

func (codeFlow) ID() string                        { return codeAuthID }
func (codeFlow) SetupSchema() provider.SetupSchema { return opaqueStateAuth{}.SetupSchema() }
func (codeFlow) Resolve(ctx context.Context, input provider.AuthInput) (provider.Credential, error) {
	return opaqueStateAuth{}.Resolve(ctx, input)
}
func (codeFlow) Refresh(ctx context.Context, input provider.AuthInput, current provider.Credential) (provider.CredentialState, error) {
	return opaqueStateAuth{}.Refresh(ctx, input, current)
}

func TestExternalGenericAuthorizationCodePersistsOpaqueCredential(t *testing.T) {
	issuer := &codeIssuer{}
	issuerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			var in struct{ State, RedirectURL, Challenge string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
				http.Error(w, "bad request", 400)
				return
			}
			parsed, err := url.Parse(in.RedirectURL)
			if err != nil {
				t.Error(err)
				http.Error(w, "bad redirect", 400)
				return
			}
			// The issuer only receives the challenge; token exchange proves the verifier later.
			challenge := in.Challenge
			probe := *parsed
			q := probe.Query()
			q.Set("state", in.State)
			q.Set("code", "premature")
			probe.RawQuery = q.Encode()
			resp, err := http.Get(probe.String())
			if err != nil {
				t.Error(err)
				http.Error(w, "callback probe failed", 500)
				return
			}
			resp.Body.Close()
			issuer.Lock()
			issuer.redirect, issuer.state, issuer.challenge = in.RedirectURL, in.State, challenge
			issuer.registrations++
			issuer.preConsentStatus = resp.StatusCode
			issuer.Unlock()
			consent, _ := url.Parse("http://issuer.test/consent")
			cq := consent.Query()
			cq.Set("redirect_uri", in.RedirectURL)
			cq.Set("state", in.State)
			cq.Set("code_challenge", challenge)
			cq.Set("code_challenge_method", "S256")
			consent.RawQuery = cq.Encode()
			_ = json.NewEncoder(w).Encode(map[string]string{"consent": consent.String()})
		case "/token":
			var in struct{ Code, Verifier, RedirectURL string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
				http.Error(w, "bad request", 400)
				return
			}
			issuer.Lock()
			defer issuer.Unlock()
			got := sha256.Sum256([]byte(in.Verifier))
			if in.Code != "issuer-code" || in.RedirectURL != issuer.redirect || base64.RawURLEncoding.EncodeToString(got[:]) != issuer.challenge {
				http.Error(w, "invalid exchange", 400)
				return
			}
			issuer.exchanges++
			_ = json.NewEncoder(w).Encode(map[string]string{"credential": `{"ticket":"code-runtime-ticket","subject":"code-subject","workspace":"workspace-7","metadata":{"issued_by":"authorization-code-fixture"}}`})
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer code-runtime-ticket" {
				t.Errorf("persisted opaque credential was not resolved: %q", r.Header.Get("Authorization"))
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"code-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuerServer.Close()
	issuer.baseURL = issuerServer.URL

	root := t.TempDir()
	manifests := filepath.Join(root, "providers")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := opaqueStateManifest(t)
	definition.ID = codeProviderID
	definition.DisplayName = "External generic authorization code"
	definition.AuthModes[0].Auth.ID = codeAuthID
	encoded, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifests, "code.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifests, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, codeModule{issuer})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Stop(ctx)
	})
	rpc := callbackRPC{t: t, config: config}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc.call("providers.create", map[string]any{"name": "Code", "prefix": "code", "baseUrl": issuerServer.URL + "/v1", "protocol": "openai_chat", "definitionID": codeProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc.call("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Code account", "credentialType": "opaque_ticket", "secret": ""}), &connection); err != nil {
		t.Fatal(err)
	}
	var metadata []provider.DefinitionMetadata
	if err := json.Unmarshal(rpc.call("providers.catalog", map[string]any{}), &metadata); err != nil {
		t.Fatal(err)
	}
	actionAuthorization := false
	for _, definition := range metadata {
		if definition.ID == codeProviderID {
			for _, mode := range definition.AuthModes {
				actionAuthorization = actionAuthorization || mode.ActionAuthorization
			}
		}
	}
	if !actionAuthorization {
		t.Fatal("provider catalog did not advertise action authorization")
	}
	view := decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": connection.ID}))
	if view.Status != "awaiting_user" || len(view.Actions) == 0 {
		t.Fatalf("setup did not publish consent action: %+v", view)
	}
	issuer.Lock()
	early := issuer.preConsentStatus
	redirect := issuer.redirect
	exchanges := issuer.exchanges
	issuer.Unlock()
	if early != http.StatusBadRequest || exchanges != 0 {
		t.Fatalf("issuer callback probe status=%d exchanges=%d", early, exchanges)
	}
	consent, err := url.Parse(view.Actions[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	if consent.Query().Get("redirect_uri") != redirect || consent.Query().Get("code_challenge_method") != "S256" || consent.Query().Get("code_challenge") == "" || consent.Query().Get("state") == "" {
		t.Fatalf("consent URL missing correlation/PKCE: %s", view.Actions[0].URL)
	}
	for _, action := range view.Actions {
		raw, _ := json.Marshal(action)
		if strings.Contains(string(raw), "Verifier") || strings.Contains(string(raw), "verifier") {
			t.Fatalf("private verifier appeared in public action: %s", raw)
		}
	}
	callback, _ := url.Parse(redirect)
	q := callback.Query()
	q.Set("state", consent.Query().Get("state"))
	q.Set("code", "issuer-code")
	callback.RawQuery = q.Encode()
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(u *url.URL) int {
		resp, e := client.Get(u.String())
		if e != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	badState := *callback
	sq := badState.Query()
	sq.Set("state", "wrong")
	badState.RawQuery = sq.Encode()
	if status := request(&badState); status == http.StatusOK {
		t.Fatal("invalid state callback was accepted")
	}
	badPath := *callback
	badPath.Path = "/wrong"
	if status := request(&badPath); status == http.StatusOK {
		t.Fatal("callback on wrong path was accepted")
	}
	badQuery := *callback
	bq := badQuery.Query()
	bq.Set("tenant", "wrong")
	badQuery.RawQuery = bq.Encode()
	if status := request(&badQuery); status == http.StatusOK {
		t.Fatal("callback with wrong fixed query was accepted")
	}
	duplicate := *callback
	dq := duplicate.Query()
	dq.Add("code", "duplicate")
	duplicate.RawQuery = dq.Encode()
	if status := request(&duplicate); status == http.StatusOK {
		t.Fatal("callback with duplicate response field was accepted")
	}
	if status := request(callback); status != http.StatusOK {
		t.Fatalf("valid callback status=%d", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view = decodeCallbackView(t, rpc.call("auth.setup.get", map[string]any{"sessionID": view.SessionID}))
		if view.Status == "completed" && view.Activation != nil && view.Activation.Status == "active" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if view.Status != "completed" || view.Activation == nil || view.Activation.Status != "active" {
		t.Fatalf("setup did not activate opaque credential: %+v", view)
	}
	if status := request(callback); status == http.StatusOK {
		t.Fatal("callback replay was accepted")
	}
	issuer.Lock()
	exchanges = issuer.exchanges
	issuer.Unlock()
	if exchanges != 1 {
		t.Fatalf("token endpoint exchanges=%d, want 1", exchanges)
	}
	stop()
	service = daemon.NewWithProviderModules(config, codeModule{issuer})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rpc.call("custom_models.upsert", map[string]any{"id": "code-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-code", "displayName": "Code model", "connectionIds": []string{connection.ID}})
	rpc.call("physical_models.upsert", map[string]any{"name": "code-physical", "identity": map[string]any{"canonicalName": "gpt-code"}, "sources": []map[string]any{{"routeId": "code-route", "fidelity": "exact"}}, "enabled": true})
	rpc.call("combo_models.upsert", map[string]any{"name": "code-role", "members": []map[string]any{{"kind": "physical", "id": "code-physical"}}, "discoverable": true, "enabled": true})
	response, err := http.Post("http://"+service.Addresses().HTTP+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"code-role","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("inference after restart status=%d", response.StatusCode)
	}
}
