package provider

import (
	"context"
	"testing"
)

func TestStaticSecretAuthIsReusablePrimitive(t *testing.T) {
	registry := NewAuthRegistry()
	flow, ok := registry.Resolve("static-secret")
	if !ok {
		t.Fatal("static-secret flow is not registered")
	}
	credential, err := flow.Resolve(context.Background(), AuthInput{ConnectionID: "conn-a", Type: "api_key", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if credential.ConnectionID != "conn-a" || credential.Secret != "secret" {
		t.Fatalf("credential=%#v", credential)
	}
	if _, err := flow.Resolve(context.Background(), AuthInput{}); err == nil {
		t.Fatal("expected missing secret error")
	}
}
