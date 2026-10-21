package kernel

import (
	"context"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func sessionNamespaceTestRoute() Route {
	route := Route{
		CredentialID: "account", DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "provider", ContractVersion: 1},
		SessionOperationRef: extensions.Ref{Kind: "operation", ID: "responses.generate", ContractVersion: 1},
		ExternalModel:       "model", BaseURL: "https://api.example.test/v1",
	}
	route.HealthGeneration.Connection = "principal-A"
	return route
}

func TestSessionStoreNamespaceIsolatesPrincipalAndEndpoint(t *testing.T) {
	route := sessionNamespaceTestRoute()
	store := NewMemorySessionStore()
	if err := store.Save(context.Background(), route, "client-session", SessionState{ResponseID: "private"}); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]Route{
		"principal": func() Route { r := route; r.HealthGeneration.Connection = "principal-B"; return r }(),
		"endpoint":  func() Route { r := route; r.BaseURL = "https://other.example.test/v1"; return r }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok, err := store.Load(context.Background(), changed, "client-session"); err != nil || ok {
				t.Fatalf("cross namespace load ok=%v err=%v", ok, err)
			}
		})
	}
	encoded, err := EncodeSessionStoreNamespace(route)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSessionStoreNamespace(encoded)
	if err != nil || decoded.PrincipalGeneration != route.HealthGeneration.Connection || decoded.Endpoint != route.BaseURL {
		t.Fatalf("decoded namespace=%#v err=%v", decoded, err)
	}
}

func TestDecodeSessionStoreNamespaceIsStrict(t *testing.T) {
	encoded, err := EncodeSessionStoreNamespace(sessionNamespaceTestRoute())
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"unknown field":     strings.TrimSuffix(encoded, "}") + `,"unexpected":true}`,
		"trailing document": encoded + ` {}`,
		"trailing garbage":  encoded + ` nope`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSessionStoreNamespace(raw); err == nil {
				t.Fatalf("accepted invalid namespace document %q", raw)
			}
		})
	}
}
