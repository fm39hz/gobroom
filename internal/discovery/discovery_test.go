package discovery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRefreshNodeNormalizesModelsCatalog(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "Provider", Prefix: "p", BaseURL: "https://provider.test/v1", Protocol: "openai_chat"}); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.ProviderNodes()
	nodeID := nodes[0].ID
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"},{"name":"model-b"},{"id":"model-a"}]}`)), Header: make(http.Header)}, nil
	})}
	result, err := (Service{Store: s, Client: client}).RefreshNode(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Models != 2 {
		t.Fatalf("result=%#v", result)
	}
	models, err := s.Models()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models=%#v", models)
	}
}
