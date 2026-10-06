package kernel

import (
	"net/url"
	"strings"
	"testing"
)

func TestHTTPJSONEndpointResolvesAPIPathAndTypedOverrides(t *testing.T) {
	endpoint := HTTPJSONEndpoint{}
	resolved, err := endpoint.Resolve("https://provider.test/v1/", "/chat/completions", EndpointOptions{Path: "/custom/chat", Query: map[string]string{"api-version": "2026-01", "tenant": "primary"}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "provider.test" || parsed.Path != "/v1/custom/chat" {
		t.Fatalf("resolved URL=%q", resolved)
	}
	if parsed.Query().Get("api-version") != "2026-01" || parsed.Query().Get("tenant") != "primary" {
		t.Fatalf("query=%v", parsed.Query())
	}
}

func TestHTTPJSONEndpointRejectsAbsoluteOperationPath(t *testing.T) {
	if _, err := (HTTPJSONEndpoint{}).Resolve("https://provider.test/v1", "https://other.test/path", EndpointOptions{}); err == nil {
		t.Fatal("absolute operation path must not escape the configured provider endpoint")
	}
}

func TestHTTPJSONEndpointPreservesEscapedModelPathSegment(t *testing.T) {
	model := url.PathEscape("vendor/model")
	resolved, err := (HTTPJSONEndpoint{}).Resolve("https://provider.test/v1", "/models/"+model+":generateContent", EndpointOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.EscapedPath(), "vendor%2Fmodel") {
		t.Fatalf("escaped model path lost: %q", parsed.EscapedPath())
	}
}
