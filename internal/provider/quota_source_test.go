package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

type quotaTransportFixture struct {
	request kernel.UpstreamRequest
}

func (*quotaTransportFixture) ID() string { return "fixture-http" }
func (t *quotaTransportFixture) Execute(_ context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	t.request = request
	return kernel.UpstreamResponse{
		Status: 200, Headers: make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{"usage":{"used":4,"limit":12,"remaining":8},"window":"ignored"}`)),
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
}
