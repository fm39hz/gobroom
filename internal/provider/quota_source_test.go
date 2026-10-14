package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type quotaTransportFixture struct {
	request kernel.UpstreamRequest
	body    string
	status  int
}

func (*quotaTransportFixture) ID() string { return "fixture-http" }
func (t *quotaTransportFixture) Execute(_ context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	t.request = request
	body := t.body
	if body == "" {
		body = `{"usage":{"used":4,"limit":12,"remaining":8},"window":"ignored"}`
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return kernel.UpstreamResponse{
		Status: status, Headers: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestHTTPJSONQuotaSourceUsesBoundEndpointTransportAndWindow(t *testing.T) {
	transport := &quotaTransportFixture{}
	route := kernel.Route{NodeID: "provider", CredentialID: "account", ExternalModel: "model", BaseURL: "https://provider.test/v1"}
	snapshots, err := (HTTPJSONQuotaSource{}).Fetch(context.Background(), QuotaRequest{
		Credential: kernel.Credential{Secret: "token"}, Route: route,
		Endpoint: kernel.HTTPJSONEndpoint{}, Transport: transport,
		EndpointOptions: kernel.EndpointOptions{Path: "/account/quota", Query: map[string]string{"window": "daily"}},
		WindowName:      "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.request.URL != "https://provider.test/v1/account/quota?window=daily" || transport.request.Headers.Get("Authorization") != "Bearer token" {
		t.Fatalf("quota request=%#v", transport.request)
	}
	if len(snapshots) != 1 || snapshots[0].WindowName != "daily" || snapshots[0].Used != 4 || snapshots[0].Limit == nil || *snapshots[0].Limit != 12 {
		t.Fatalf("quota snapshots=%#v", snapshots)
	}
	authTransport := &quotaTransportFixture{}
	configured := HTTPJSONQuotaSource{Options: HTTPJSONQuotaOptions{CredentialHeader: "Authorization"}}
	if _, err := configured.Fetch(context.Background(), QuotaRequest{
		Credential: kernel.Credential{Secret: "token"}, Route: route,
		Endpoint: kernel.HTTPJSONEndpoint{}, Transport: authTransport,
	}); err != nil {
		t.Fatal(err)
	}
	if authTransport.request.Headers.Get("Authorization") != "Bearer token" {
		t.Fatalf("explicit Authorization header lost the default bearer scheme: %#v", authTransport.request.Headers)
	}
}

func TestHTTPJSONQuotaOptionsExpandNestedWindowsWithoutProviderBranches(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	body := []byte(`{"quotas":{"5h":{"remainingPercentage":60,"fractionReported":true,"resetAt":"` + reset.Format(time.RFC3339) + `"},"weekly":{"remainingPercentage":25,"fractionReported":true},"unreported":{"remainingPercentage":0,"fractionReported":false}}}`)
	options := HTTPJSONQuotaOptions{Windows: []HTTPJSONQuotaWindow{{
		ItemsPath: "/quotas", RemainingPercentPath: "/remainingPercentage", ResetAtPath: "/resetAt",
		IgnoreWhenPath: "/fractionReported", IgnoreWhenValues: []string{"false"},
	}}}
	route := kernel.Route{NodeID: "provider", CredentialID: "account", ExternalModel: "model"}
	snapshots, err := DecodeQuotaJSONWithOptions(body, route, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].WindowName != "5h" || snapshots[1].WindowName != "weekly" {
		t.Fatalf("dynamic quota windows=%#v", snapshots)
	}
	if snapshots[0].Limit == nil || *snapshots[0].Limit != 100 || snapshots[0].Remaining == nil || *snapshots[0].Remaining != 60 || snapshots[0].Used != 40 || snapshots[0].ResetAt == nil || !snapshots[0].ResetAt.Equal(reset) {
		t.Fatalf("normalized 5h quota window=%#v", snapshots[0])
	}
	if snapshots[1].Remaining == nil || *snapshots[1].Remaining != 25 || snapshots[1].Used != 75 {
		t.Fatalf("normalized weekly quota window=%#v", snapshots[1])
	}
}

func TestManifestBindsTypedQuotaWindowOptionsAndCredentialHeader(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "configured-quota", Version: "1", DisplayName: "Configured quota source",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationQuota: {
			Endpoint:           PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:          PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			QuotaSource:        PrimitiveRef{Kind: PrimitiveQuotaSource, ID: "http-json-quota", ContractVersion: 1},
			EndpointOptions:    kernel.EndpointOptions{Path: "/account/usage"},
			QuotaSourceOptions: json.RawMessage(`{"credentialHeader":"x-api-key","windows":[{"itemsPath":"/quotas","remainingPercentPath":"/remaining","resetAtPath":"/reset"}]}`),
		}},
	}
	manifest, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[RuntimeBindingKey(definition.ID, OperationQuota)]
	wantRef := runtimeQuotaSourceRef(definition.ID, OperationQuota, definition.Operations[OperationQuota].QuotaSource)
	if binding.QuotaSourceRef != wantRef {
		t.Fatalf("quota source options were not bound to a definition-scoped ref: got=%s want=%s", binding.QuotaSourceRef.Key(), wantRef.Key())
	}
	transport := &quotaTransportFixture{body: `{"quotas":{"monthly":{"remaining":7,"reset":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}}}`}
	resolved := registry.QuotaSourcesForBindings(bindings)[binding.QuotaSourceRef]
	snapshots, err := resolved.Fetch(context.Background(), QuotaRequest{
		Credential: kernel.Credential{Secret: "fixture-key"},
		Route:      kernel.Route{NodeID: "provider", CredentialID: "account", ExternalModel: "model", BaseURL: "https://provider.test/v1"},
		Endpoint:   binding.Endpoint, Transport: transport, EndpointOptions: binding.EndpointOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.request.URL != "https://provider.test/v1/account/usage" || transport.request.Headers.Get("x-api-key") != "fixture-key" {
		t.Fatalf("configured quota request=%#v", transport.request)
	}
	if len(snapshots) != 1 || snapshots[0].WindowName != "monthly" || snapshots[0].Remaining == nil || *snapshots[0].Remaining != 7 {
		t.Fatalf("configured quota snapshots=%#v", snapshots)
	}
}
