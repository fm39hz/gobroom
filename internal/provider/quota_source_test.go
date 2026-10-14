package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

type quotaReportFixture struct {
	body   string
	status int
}

type multiQuotaTransportFixture struct {
	requests  []kernel.UpstreamRequest
	responses map[string]quotaReportFixture
}

func (*multiQuotaTransportFixture) ID() string { return "multi-quota-fixture" }
func (t *multiQuotaTransportFixture) Execute(_ context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	t.requests = append(t.requests, request)
	response, ok := t.responses[request.URL]
	if !ok {
		return kernel.UpstreamResponse{}, fmt.Errorf("unexpected quota endpoint %q", request.URL)
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return kernel.UpstreamResponse{Status: status, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader(response.body))}, nil
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

func TestHTTPJSONQuotaSourceComposesMultipleReportsWithPartialSuccess(t *testing.T) {
	reset := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second)
	keyURL := "https://openrouter.test/api/v1/key"
	creditsURL := "https://openrouter.test/api/v1/credits"
	keyBody := `{"data":{"limit":25,"limit_remaining":8,"limit_reset":` + strconv.FormatInt(reset.Unix(), 10) + `,"usage":17,"usage_daily":3}}`
	creditsBody := `{"data":{"total_credits":100,"total_usage":40}}`
	options := HTTPJSONQuotaOptions{FailureMode: QuotaReportsKeepAvailable, Reports: []HTTPJSONQuotaReport{
		{EndpointOptions: kernel.EndpointOptions{Path: "/key"}, Windows: []HTTPJSONQuotaWindow{
			{Name: "key-cap", LimitPath: "/data/limit", RemainingPath: "/data/limit_remaining", UsedPath: "/data/usage", ResetAtPath: "/data/limit_reset"},
			{Name: "daily-usage", UsedPath: "/data/usage_daily"},
		}},
		{EndpointOptions: kernel.EndpointOptions{Path: "/credits"}, Windows: []HTTPJSONQuotaWindow{{
			Name: "account-credits", LimitPath: "/data/total_credits", UsedPath: "/data/total_usage", RemainingFromLimitUsed: true,
		}}},
	}}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	definition := ProviderDefinition{ContractVersion: 1, ID: "openrouter-report-fixture", Version: "1", DisplayName: "OpenRouter quota reports",
		Auth: PrimitiveRef{Kind: PrimitiveAuth, ID: "static-secret", ContractVersion: 1},
		Operations: map[Operation]OperationBinding{OperationQuota: {
			Endpoint:           PrimitiveRef{Kind: PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:          PrimitiveRef{Kind: PrimitiveTransport, ID: "http", ContractVersion: 1},
			QuotaSource:        PrimitiveRef{Kind: PrimitiveQuotaSource, ID: "http-json-quota", ContractVersion: 1},
			QuotaSourceOptions: optionsJSON,
		}},
	}
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	runtimeBindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := runtimeBindings[RuntimeBindingKey(definition.ID, OperationQuota)]
	wantSourceRef := runtimeQuotaSourceRef(definition.ID, OperationQuota, definition.Operations[OperationQuota].QuotaSource)
	if binding.QuotaSourceRef != wantSourceRef {
		t.Fatalf("multi-report source did not bind per definition: %s", binding.QuotaSourceRef.Key())
	}
	source := registry.QuotaSourcesForBindings(runtimeBindings)[binding.QuotaSourceRef]
	configuredSource, ok := source.(HTTPJSONQuotaSource)
	if !ok || len(configuredSource.Options.Reports) != 2 || configuredSource.Options.FailureMode != QuotaReportsKeepAvailable {
		t.Fatalf("manifest quota source options were not bound: %#v", source)
	}
	route := kernel.Route{NodeID: "openrouter", CredentialID: "account", ExternalModel: "model", BaseURL: "https://openrouter.test/api/v1"}
	transport := &multiQuotaTransportFixture{responses: map[string]quotaReportFixture{
		keyURL:     {body: keyBody},
		creditsURL: {status: http.StatusServiceUnavailable, body: `{"error":"temporarily unavailable"}`},
	}}
	snapshots, err := source.Fetch(context.Background(), QuotaRequest{
		Credential: kernel.Credential{Secret: "openrouter-key"}, Route: route,
		Endpoint: binding.Endpoint, Transport: transport, EndpointOptions: binding.EndpointOptions,
	})
	if err != nil {
		t.Fatalf("best-effort quota source discarded a successful report: %v", err)
	}
	if len(transport.requests) != 2 || transport.requests[0].Headers.Get("Authorization") != "Bearer openrouter-key" || transport.requests[1].URL != creditsURL {
		t.Fatalf("composed quota requests=%#v", transport.requests)
	}
	if len(snapshots) != 2 || snapshots[0].WindowName != "key-cap" || snapshots[1].WindowName != "daily-usage" {
		t.Fatalf("partial quota snapshots=%#v", snapshots)
	}
	if snapshots[0].Remaining == nil || *snapshots[0].Remaining != 8 || snapshots[0].Limit == nil || *snapshots[0].Limit != 25 || snapshots[0].ResetAt == nil || !snapshots[0].ResetAt.Equal(reset) {
		t.Fatalf("OpenRouter key quota normalization=%#v", snapshots[0])
	}

	sourceFailAll := configuredSource
	sourceFailAll.Options.FailureMode = QuotaReportsFailAll
	transport = &multiQuotaTransportFixture{responses: map[string]quotaReportFixture{
		keyURL:     {body: keyBody},
		creditsURL: {status: http.StatusServiceUnavailable, body: `{"error":"temporarily unavailable"}`},
	}}
	if snapshots, err = sourceFailAll.Fetch(context.Background(), QuotaRequest{Credential: kernel.Credential{Secret: "openrouter-key"}, Route: route, Endpoint: binding.Endpoint, Transport: transport, EndpointOptions: binding.EndpointOptions}); err == nil || snapshots != nil {
		t.Fatalf("fail-all quota mode returned partial results: snapshots=%#v err=%v", snapshots, err)
	}

	transport = &multiQuotaTransportFixture{responses: map[string]quotaReportFixture{
		keyURL:     {body: keyBody},
		creditsURL: {body: creditsBody},
	}}
	snapshots, err = sourceFailAll.Fetch(context.Background(), QuotaRequest{Credential: kernel.Credential{Secret: "openrouter-key"}, Route: route, Endpoint: binding.Endpoint, Transport: transport, EndpointOptions: binding.EndpointOptions})
	if err != nil || len(snapshots) != 3 || snapshots[2].WindowName != "account-credits" || snapshots[2].Remaining == nil || *snapshots[2].Remaining != 60 {
		t.Fatalf("successful multi-report normalization: snapshots=%#v err=%v", snapshots, err)
	}
}

func TestDeepSeekQuotaManifestAggregatesCurrencyBalanceAvailability(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionFile("../../manifests/builtin/generic.json"); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[RuntimeBindingKey("deepseek", OperationQuota)]
	if binding.QuotaSource == nil || binding.EndpointOptions.Path != "/user/balance" {
		t.Fatalf("DeepSeek quota binding=%#v", binding)
	}
	source := registry.QuotaSourcesForBindings(bindings)[binding.QuotaSourceRef]
	transport := &quotaTransportFixture{}
	request := QuotaRequest{
		Credential: kernel.Credential{Secret: "deepseek-key"},
		Route:      kernel.Route{NodeID: "deepseek", CredentialID: "account", ExternalModel: "deepseek-chat", BaseURL: "https://api.deepseek.com"},
		Endpoint:   binding.Endpoint, Transport: transport, EndpointOptions: binding.EndpointOptions,
	}
	for _, fixture := range []struct {
		name      string
		body      string
		remaining float64
		count     int
	}{
		{name: "USD zero but CNY positive", body: `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"0.00"},{"currency":"CNY","total_balance":"100.00"}]}`, remaining: 1, count: 1},
		{name: "all currencies exhausted", body: `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"0.00"},{"currency":"CNY","total_balance":"0.00"}]}`, remaining: 0, count: 1},
		{name: "provider unavailable overrides positive balance", body: `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"50.00"}]}`, remaining: 0, count: 1},
		{name: "no balance rows is unknown", body: `{"is_available":true,"balance_infos":[]}`, count: 0},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			transport.body = fixture.body
			snapshots, err := source.Fetch(context.Background(), request)
			if err != nil || len(snapshots) != fixture.count {
				t.Fatalf("DeepSeek quota snapshots=%#v err=%v", snapshots, err)
			}
			if transport.request.URL != "https://api.deepseek.com/user/balance" || transport.request.Headers.Get("Authorization") != "Bearer deepseek-key" {
				t.Fatalf("DeepSeek quota request=%#v", transport.request)
			}
			if fixture.count > 0 && (snapshots[0].WindowName != "balance-availability" || snapshots[0].Remaining == nil || *snapshots[0].Remaining != fixture.remaining) {
				t.Fatalf("DeepSeek availability snapshot=%#v", snapshots[0])
			}
		})
	}
}

func TestDeepSeekQuotaManifestAggregatesAlternativeCurrencyBalances(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadDefinitionFile("../../manifests/builtin/generic.json"); err != nil {
		t.Fatal(err)
	}
	bindings, err := registry.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[RuntimeBindingKey("deepseek", OperationQuota)]
	if binding.QuotaSource == nil || binding.EndpointOptions.Path != "/user/balance" {
		t.Fatalf("DeepSeek quota binding=%#v", binding)
	}
	source := registry.QuotaSourcesForBindings(bindings)[binding.QuotaSourceRef]
	transport := &quotaTransportFixture{}
	request := QuotaRequest{
		Credential: kernel.Credential{Secret: "deepseek-key"},
		Route:      kernel.Route{NodeID: "deepseek", CredentialID: "account", ExternalModel: "deepseek-chat", BaseURL: "https://api.deepseek.com"},
		Endpoint:   binding.Endpoint, Transport: transport, EndpointOptions: binding.EndpointOptions,
	}
	for _, fixture := range []struct {
		name      string
		body      string
		remaining float64
		wantCount int
	}{
		{name: "USD zero but CNY positive", body: `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"0.00"},{"currency":"CNY","total_balance":"100.00"}]}`, remaining: 1, wantCount: 1},
		{name: "all currencies exhausted", body: `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"0.00"},{"currency":"CNY","total_balance":"0.00"}]}`, remaining: 0, wantCount: 1},
		{name: "provider unavailable overrides positive balance", body: `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"50.00"}]}`, remaining: 0, wantCount: 1},
		{name: "no balance rows is unknown", body: `{"is_available":true,"balance_infos":[]}`, wantCount: 0},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			transport.body = fixture.body
			snapshots, err := source.Fetch(context.Background(), request)
			if err != nil || len(snapshots) != fixture.wantCount {
				t.Fatalf("DeepSeek quota snapshots=%#v err=%v", snapshots, err)
			}
			if fixture.wantCount == 1 && (snapshots[0].WindowName != "balance-availability" || snapshots[0].Remaining == nil || *snapshots[0].Remaining != fixture.remaining) {
				t.Fatalf("DeepSeek availability snapshot=%#v", snapshots[0])
			}
		})
	}
}
