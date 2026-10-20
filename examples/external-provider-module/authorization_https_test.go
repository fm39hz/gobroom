package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
	"golang.org/x/oauth2"
)

const (
	httpsCodeProviderID = "example-external-https-code"
	httpsCodeAuthID     = "example-external-https-code-auth"
)

var httpsCodeSchemaRef = provider.Ref{Kind: provider.SchemaKind, ID: "example.external-https-code.input", ContractVersion: 1}

type httpsCodeFixture struct {
	sync.Mutex
	issuerURL, redirect, challenge string
	publicClient                   *http.Client
	registrations, exchanges       int
	probeStatus                    int
}

type httpsCodeModule struct {
	fixture *httpsCodeFixture
	origin  string
}

func (httpsCodeModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.external-https-code", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (m httpsCodeModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterSchemaDocument(httpsCodeSchemaRef, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"code":{"type":"string"},"state":{"type":"string"}},"additionalProperties":false}`)); err != nil {
		return err
	}
	return runtime.RegisterAuthFactory(httpsCodeAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return provider.NewAuthorizationCodeDriver(httpsOpaqueCodeFlow{}, provider.AuthorizationCodeOptions{
			RedirectURL: m.origin + "/return?tenant=tls", CallbackRequired: true, InputSchemaRef: httpsCodeSchemaRef,
		}, func(ctx context.Context, request provider.AuthorizationCodeRequest) (string, error) {
			challenge := oauth2.S256ChallengeFromVerifier(request.Verifier)
			body, _ := json.Marshal(struct{ State, RedirectURL, Challenge string }{request.State, request.RedirectURL, challenge})
			registration, err := http.NewRequestWithContext(ctx, http.MethodPost, m.fixture.issuerURL+"/register", strings.NewReader(string(body)))
			if err != nil {
				return "", err
			}
			registration.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(registration)
			if err != nil {
				return "", err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return "", fmt.Errorf("issuer registration status %d", response.StatusCode)
			}
			var result struct {
				Consent string `json:"consent"`
			}
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				return "", err
			}
			return result.Consent, nil
		}, func(ctx context.Context, exchange provider.AuthorizationCodeExchange) (provider.CredentialState, error) {
			body, _ := json.Marshal(struct{ Code, Verifier, RedirectURL string }{exchange.Code, exchange.Verifier, exchange.RedirectURL})
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.fixture.issuerURL+"/token", strings.NewReader(string(body)))
			if err != nil {
				return provider.CredentialState{}, err
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return provider.CredentialState{}, err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return provider.CredentialState{}, fmt.Errorf("issuer token status %d", response.StatusCode)
			}
			var result struct {
				Credential string `json:"credential"`
			}
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				return provider.CredentialState{}, err
			}
			return provider.NewCredentialState(result.Credential)
		})
	})
}

type httpsOpaqueCodeFlow struct{}

func (httpsOpaqueCodeFlow) ID() string                        { return httpsCodeAuthID }
func (httpsOpaqueCodeFlow) SetupSchema() provider.SetupSchema { return opaqueStateAuth{}.SetupSchema() }
func (httpsOpaqueCodeFlow) Resolve(ctx context.Context, in provider.AuthInput) (provider.Credential, error) {
	return opaqueStateAuth{}.Resolve(ctx, in)
}
func (httpsOpaqueCodeFlow) Refresh(ctx context.Context, in provider.AuthInput, current provider.Credential) (provider.CredentialState, error) {
	return opaqueStateAuth{}.Refresh(ctx, in, current)
}

func TestExternalAuthorizationCodeThroughPublicHTTPSProxy(t *testing.T) {
	fixture := &httpsCodeFixture{}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			var in struct{ State, RedirectURL, Challenge string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			u, err := url.Parse(in.RedirectURL)
			if err != nil {
				http.Error(w, "bad redirect", 400)
				return
			}
			probe := *u
			q := probe.Query()
			q.Set("state", in.State)
			q.Set("code", "premature")
			probe.RawQuery = q.Encode()
			resp, err := fixture.publicClient.Get(probe.String())
			if err != nil {
				http.Error(w, "callback probe failed", 500)
				return
			}
			resp.Body.Close()
			fixture.Lock()
			fixture.redirect, fixture.challenge, fixture.probeStatus = in.RedirectURL, in.Challenge, resp.StatusCode
			fixture.registrations++
			fixture.Unlock()
			consent, _ := url.Parse("https://issuer.test/consent")
			cq := consent.Query()
			cq.Set("redirect_uri", in.RedirectURL)
			cq.Set("state", in.State)
			cq.Set("code_challenge", in.Challenge)
			cq.Set("code_challenge_method", "S256")
			consent.RawQuery = cq.Encode()
			_ = json.NewEncoder(w).Encode(map[string]string{"consent": consent.String()})
		case "/token":
			var in struct{ Code, Verifier, RedirectURL string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			fixture.Lock()
			defer fixture.Unlock()
			if in.Code != "issuer-code" || in.RedirectURL != fixture.redirect || oauth2.S256ChallengeFromVerifier(in.Verifier) != fixture.challenge {
				http.Error(w, "invalid exchange", 400)
				return
			}
			fixture.exchanges++
			_ = json.NewEncoder(w).Encode(map[string]string{"credential": `{"ticket":"https-tls-ticket","subject":"https-subject"}`})
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer https-tls-ticket" {
				http.Error(w, "unauthorized", 401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"tls","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	fixture.issuerURL = issuer.URL

	var daemonRef atomic.Pointer[daemon.Daemon]
	publicProxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := daemonRef.Load()
		if d == nil {
			http.Error(w, "not ready", 503)
			return
		}
		target, _ := url.Parse("http://" + d.Addresses().CallbackIngress)
		proxy := httputil.NewSingleHostReverseProxy(target)
		director := proxy.Director
		proxy.Director = func(req *http.Request) { originalHost := req.Host; director(req); req.Host = originalHost }
		proxy.ServeHTTP(w, r)
	}))
	defer publicProxy.Close()
	fixture.publicClient = publicProxy.Client()

	root := t.TempDir()
	manifests := filepath.Join(root, "providers")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := opaqueStateManifest(t)
	definition.ID = httpsCodeProviderID
	definition.DisplayName = "External HTTPS code"
	definition.AuthModes[0].Auth.ID = httpsCodeAuthID
	encoded, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifests, "https-code.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifests,
		HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPToken: "https-data-key",
		CallbackIngress: daemon.AuthorizationCallbackIngressConfig{PublicOrigin: publicProxy.URL, ListenAddress: "127.0.0.1:0"}}
	module := httpsCodeModule{fixture: fixture, origin: publicProxy.URL}
	service := daemon.NewWithProviderModules(config, module)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	daemonRef.Store(service)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Stop(ctx)
	}()
	if service.Addresses().CallbackIngress == "" || service.Addresses().CallbackIngress == "127.0.0.1:0" {
		t.Fatalf("callback ingress address not bound: %+v", service.Addresses())
	}
	firstIngressAddr := service.Addresses().CallbackIngress
	rpc := callbackRPC{t: t, config: config}
	var node, connection struct{ ID string }
	if err := json.Unmarshal(rpc.call("providers.create", map[string]any{"name": "HTTPS code", "prefix": "https-code", "baseUrl": issuer.URL + "/v1", "protocol": "openai_chat", "definitionID": httpsCodeProviderID}), &node); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc.call("connections.create", map[string]any{"providerNodeID": node.ID, "name": "HTTPS account", "credentialType": "opaque_ticket", "secret": ""}), &connection); err != nil {
		t.Fatal(err)
	}
	view := decodeCallbackView(t, rpc.call("auth.setup.start", map[string]any{"connectionID": connection.ID}))
	if view.Status != "awaiting_user" || len(view.Actions) == 0 || len(view.CallbackModes) == 0 || view.CallbackModes[0].Mode != daemon.AuthorizationCallbackProxiedHTTPS {
		t.Fatalf("setup did not expose proxied HTTPS callback mode: %+v", view)
	}
	rawView, _ := json.Marshal(view)
	if strings.Contains(string(rawView), service.Addresses().CallbackIngress) || strings.Contains(string(rawView), "nonce") || strings.Contains(string(rawView), "correlation") {
		t.Fatalf("public callback view disclosed private ingress/correlation: %s", rawView)
	}
	fixture.Lock()
	callbackRedirect, earlyStatus := fixture.redirect, fixture.probeStatus
	fixture.Unlock()
	if callbackRedirect != publicProxy.URL+"/return?tenant=tls" || earlyStatus == http.StatusOK {
		t.Fatalf("redirect=%q pre-grant probe status=%d", callbackRedirect, earlyStatus)
	}
	consent, err := url.Parse(view.Actions[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(consent.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	params := callback.Query()
	params.Set("state", consent.Query().Get("state"))
	params.Set("code", "issuer-code")
	callback.RawQuery = params.Encode()
	client := publicProxy.Client()
	request := func(u *url.URL, forgedForwarded bool, hostOverride string) int {
		req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
		if hostOverride != "" {
			req.Host = hostOverride
		}
		if forgedForwarded {
			req.Header.Set("X-Forwarded-Host", "attacker.invalid")
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		resp, e := client.Do(req)
		if e != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	bad := *callback
	q := bad.Query()
	q.Set("state", "wrong-nonce")
	bad.RawQuery = q.Encode()
	if status := request(&bad, false, ""); status == http.StatusOK {
		t.Fatal("wrong nonce callback was accepted")
	}
	bad = *callback
	if status := request(&bad, true, "attacker.invalid"); status == http.StatusOK {
		t.Fatal("wrong Host/forged forwarded callback was accepted")
	}
	bad = *callback
	bad.Path = "/wrong"
	if status := request(&bad, false, ""); status == http.StatusOK {
		t.Fatal("wrong path callback was accepted")
	}
	bad = *callback
	q = bad.Query()
	q.Set("unknown", "x")
	bad.RawQuery = q.Encode()
	if status := request(&bad, false, ""); status == http.StatusOK {
		t.Fatal("unknown callback key was accepted")
	}
	fixture.Lock()
	exchanges := fixture.exchanges
	fixture.Unlock()
	if exchanges != 0 {
		t.Fatalf("invalid callback caused %d exchanges", exchanges)
	}
	if status := request(callback, false, ""); status != http.StatusOK {
		t.Fatalf("valid public HTTPS callback status=%d", status)
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
		t.Fatalf("callback did not activate credential: %+v", view)
	}
	if status := request(callback, false, ""); status == http.StatusOK {
		t.Fatal("callback replay was accepted")
	}
	fixture.Lock()
	exchanges = fixture.exchanges
	fixture.Unlock()
	if exchanges != 1 {
		t.Fatalf("exchange count=%d, want 1", exchanges)
	}
	if err := json.Unmarshal(rpc.call("custom_models.upsert", map[string]any{"id": "https-tls-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-https-tls", "displayName": "HTTPS TLS model", "connectionIds": []string{connection.ID}}), new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc.call("physical_models.upsert", map[string]any{"name": "https-tls-physical", "identity": map[string]any{"canonicalName": "gpt-https-tls"}, "sources": []map[string]any{{"routeId": "https-tls-route", "fidelity": "exact"}}, "enabled": true}), new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc.call("combo_models.upsert", map[string]any{"name": "https-tls-role", "members": []map[string]any{{"kind": "physical", "id": "https-tls-physical"}}, "discoverable": true, "enabled": true}), new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	infer := func() int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+service.Addresses().HTTP+"/v1/chat/completions", strings.NewReader(`{"model":"https-tls-role","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer https-data-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if status := infer(); status != http.StatusOK {
		t.Fatalf("inference with dataplane API key status=%d", status)
	}

	for _, path := range []string{"/v1/models", "/admin/status"} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+service.Addresses().CallbackIngress+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("callback private plane exposed %s", path)
			}
		}
	}
	dataReq, _ := http.NewRequest(http.MethodGet, "http://"+service.Addresses().HTTP+"/v1/models", nil)
	dataResp, err := http.DefaultClient.Do(dataReq)
	if err != nil {
		t.Fatal(err)
	}
	dataResp.Body.Close()
	if dataResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("data plane without API key status=%d", dataResp.StatusCode)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := service.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatal(err)
	}
	stopCancel()
	if conn, err := net.DialTimeout("tcp", firstIngressAddr, time.Second); err == nil {
		conn.Close()
		t.Fatalf("callback ingress still accepts connections after stop at %s", firstIngressAddr)
	}
	service = daemon.NewWithProviderModules(config, module)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	daemonRef.Store(service)
	if status := infer(); status != http.StatusOK {
		t.Fatalf("persisted-ticket inference after restart status=%d", status)
	}
	stopCtx, stopCancel = context.WithTimeout(context.Background(), 5*time.Second)
	if err := service.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatal(err)
	}
	stopCancel()
	if service.Addresses().CallbackIngress != "" {
		t.Fatalf("callback ingress remained published after stop: %+v", service.Addresses())
	}
}
