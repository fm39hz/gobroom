package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorouter/gorouter/internal/store"
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
