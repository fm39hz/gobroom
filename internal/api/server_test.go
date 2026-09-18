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

	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestModelsOnlyExposePublishedReferences(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertPublicModel(store.PublicModel{Name: "tech-lead", TargetRef: "combo:tech-lead"}); err != nil {
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
INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('node-a','A',?,'openai_chat','a');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name) VALUES('route:a','node-a','custom','upstream-model','Model A');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref) VALUES('conn-a','node-a','primary','api_key','secret');
		INSERT INTO published_models(name,target_ref) VALUES('public-a','route:a');`, upstreamURL); err != nil {
		t.Fatal(err)
	}
	server := NewServer(s)
	k, err := kernel.New(server.Control().Snapshot(), kernel.AlwaysOpenGate{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters[kernel.ProtocolOpenAIChat] = openai.Chat{}
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
