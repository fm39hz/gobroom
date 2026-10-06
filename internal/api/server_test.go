package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestModelsOnlyExposePublishedReferences(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertComboModel(store.ComboModel{Name: "tech-lead", Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(s)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0]["id"] != "tech-lead" {
		t.Fatalf("unexpected /models: %#v", body.Data)
	}

	payload, _ := json.Marshal(map[string]string{"model": "deepseek-v4-flash"})
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("hidden model status=%d", rec.Code)
	}

	payload, _ = json.Marshal(map[string]string{"model": "tech-lead"})
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("published model status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProviderPrefixCollisionIsRejected(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := NewServer(s)
	body := bytes.NewBufferString(`{"name":"First","prefix":"g4f","baseUrl":"https://example.test/v1","protocol":"openai_chat"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/providers", body)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	body = bytes.NewBufferString(`{"name":"Second","prefix":"g4f","baseUrl":"https://example.test/v1","protocol":"openai_chat"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/providers", body)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("collision status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicProviderDefaultsToMessagesDefinition(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := NewServer(s)
	first := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewBufferString(`{"name":"OpenAI","prefix":"openai","baseUrl":"https://api.openai.com/v1","protocol":"openai_chat"}`))
	firstRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("OpenAI create status=%d body=%s", firstRec.Code, firstRec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewBufferString(`{"name":"Anthropic","prefix":"anthropic","baseUrl":"https://api.anthropic.com/v1","protocol":"anthropic"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	items, err := s.ProviderNodes()
	definitions := map[string]string{}
	for _, item := range items {
		definitions[item.Prefix] = item.DefinitionID
	}
	if err != nil || len(items) != 2 || definitions["openai"] != "openai-compatible-chat" || definitions["anthropic"] != "anthropic-messages" {
		t.Fatalf("providers=%#v err=%v", items, err)
	}
}

func TestDataPlaneBearerTokenIsOptionalButEnforcedWhenConfigured(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := NewServer(s)
	server.SetDataPlaneToken("secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	server.HandlerWithOptions(HandlerOptions{DataPlane: true}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	server.HandlerWithOptions(HandlerOptions{DataPlane: true}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated status=%d", rec.Code)
	}
}

func TestOpenAIChatVerticalSliceReachesUpstream(t *testing.T) {
	upstreamHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization=%q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if body["model"] != "upstream-model" {
			t.Errorf("upstream model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable in test environment: %v", err)
	}
	upstreamServer := &http.Server{Handler: upstreamHandler}
	go upstreamServer.Serve(listener)
	defer upstreamServer.Close()
	upstreamURL := "http://" + listener.Addr().String()

	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol,prefix,definition_id) VALUES('node-a','A',?,'openai_chat','a','openai-compatible-chat');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','custom','upstream-model','Model A');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref) VALUES('conn-a','node-a','primary','api_key','secret');
		INSERT INTO physical_models(name,discoverable,enabled) VALUES('physical-a',0,1);
		INSERT INTO physical_model_sources(physical_name,position,route_id,fidelity) VALUES('physical-a',0,'route:a','exact');`, upstreamURL); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "public-a", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "physical-a"}}, Strategy: store.StrategySpec{ID: "ordered-fallback"}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithRuntimeBindings(s, bindings)
	k, err := kernel.New(server.Control().Snapshot(), kernel.AlwaysOpenGate{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	binding := bindings[provider.RuntimeBindingKey("openai-compatible-chat", provider.OperationChat)]
	k.Adapters[binding.AdapterID] = binding.Adapter
	k.ResolveCredential = func(_ context.Context, route kernel.Route) (kernel.Credential, error) {
		credential, ok := s.ConnectionCredentialByID(route.CredentialID)
		if !ok {
			return kernel.Credential{}, fmt.Errorf("credential missing")
		}
		return kernel.Credential{Secret: credential.Secret}, nil
	}
	server.SetExecutor(func(ctx context.Context, request normalize.Request, writer http.ResponseWriter) error {
		return k.Execute(ctx, request, kernel.Credential{}, writer)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"public-a","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	recorder := httptest.NewRecorder()
	server.HandlerWithOptions(HandlerOptions{DataPlane: true}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"content":"ok"`)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAnthropicMessagesAPIKeyVerticalSliceReachesUpstream(t *testing.T) {
	upstreamHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-secret" {
			t.Errorf("x-api-key=%q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version=%q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if body["model"] != "claude-sonnet" {
			t.Errorf("upstream model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("network listener unavailable in test environment: %v", err)
	}
	upstreamServer := &http.Server{Handler: upstreamHandler}
	go upstreamServer.Serve(listener)
	defer upstreamServer.Close()
	upstreamURL := "http://" + listener.Addr().String() + "/v1"

	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`
INSERT INTO provider_nodes(id,name,base_url,protocol,prefix,definition_id) VALUES('node-anthropic','Anthropic',?,'anthropic','anthropic','anthropic-messages');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:anthropic','node-anthropic','custom','claude-sonnet','Claude Sonnet');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref) VALUES('conn-anthropic','node-anthropic','primary','api_key','anthropic-secret');
		INSERT INTO physical_models(name,discoverable,enabled) VALUES('claude-sonnet-physical',0,1);
INSERT INTO physical_model_sources(physical_name,position,route_id,fidelity) VALUES('claude-sonnet-physical',0,'route:anthropic','exact');`, upstreamURL); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertComboModel(store.ComboModel{Name: "claude-sonnet-public", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "claude-sonnet-physical"}}, Strategy: store.StrategySpec{ID: "ordered-fallback"}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithRuntimeBindings(s, bindings)
	k, err := kernel.New(server.Control().Snapshot(), kernel.AlwaysOpenGate{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	binding := bindings[provider.RuntimeBindingKey("anthropic-messages", provider.OperationMessages)]
	k.Adapters[binding.AdapterID] = binding.Adapter
	k.ResolveCredential = func(_ context.Context, route kernel.Route) (kernel.Credential, error) {
		credential, ok := s.ConnectionCredentialByID(route.CredentialID)
		if !ok {
			return kernel.Credential{}, fmt.Errorf("credential missing")
		}
		return kernel.Credential{Secret: credential.Secret}, nil
	}
	server.SetExecutor(func(ctx context.Context, request normalize.Request, writer http.ResponseWriter) error {
		return k.Execute(ctx, request, kernel.Credential{}, writer)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-public","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	server.HandlerWithOptions(HandlerOptions{DataPlane: true}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"text":"ok"`)) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
