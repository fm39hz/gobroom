package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAuthorizationCallbackAllocationPort(t *testing.T) {
	callback := validCallback()
	callback.URL = "http://127.0.0.1:0/callback?fixed=private-value"
	if err := callback.Validate(); err != nil {
		t.Fatalf("port zero callback should validate: %v", err)
	}
	if !callback.NeedsAllocation() {
		t.Fatal("port zero callback should request allocation")
	}
	callback.URL = "http://127.0.0.1:43123/callback"
	if callback.NeedsAllocation() {
		t.Fatal("fixed port should not request allocation")
	}
	for _, rawURL := range []string{"http://localhost/callback", "https://localhost:0/callback", "http://example.invalid:0/callback"} {
		callback.URL = rawURL
		if err := callback.Validate(); err == nil {
			t.Errorf("invalid callback URL accepted: %s", rawURL)
		}
	}
	callback.URL = "not a URL"
	if callback.NeedsAllocation() {
		t.Fatal("invalid URL should not request allocation")
	}
}

func TestAuthorizationCallbackResourceAndEventRedact(t *testing.T) {
	resource := AuthorizationCallbackResource{
		ActionID: "submit", URL: "http://127.0.0.1:43123/callback?fixed=private-query-value",
		Mode: AuthorizationCallbackLoopback,
	}
	for _, rendered := range []string{resource.String(), fmt.Sprintf("%#v", resource)} {
		if strings.Contains(rendered, "private") || strings.Contains(rendered, "127.0.0.1") {
			t.Fatalf("resource leaked in %q", rendered)
		}
	}
	data, err := json.Marshal(resource)
	if err != nil || string(data) != `"[redacted]"` {
		t.Fatalf("resource JSON = %s, %v", data, err)
	}
	event := AuthorizationEvent{Kind: AuthorizationCallbacksReady, Callbacks: []AuthorizationCallbackResource{resource}}
	data, err = json.Marshal(event)
	if err != nil || strings.Contains(string(data), "private") || strings.Contains(string(data), "127.0.0.1") {
		t.Fatalf("event serialization leaked resource: %s, %v", data, err)
	}
}
