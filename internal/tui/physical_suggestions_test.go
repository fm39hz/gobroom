package tui

import "testing"

func TestSuggestPhysicalNameGroupsEquivalentProviderIDs(t *testing.T) {
	routes := []discoveredRoute{
		{ProviderPrefix: "xkiro", ExternalID: "qwen/qwen3.7-max:free"},
		{ProviderPrefix: "ocg", ExternalID: "qwen3.7-max"},
		{ProviderPrefix: "g4f", ExternalID: "Qwen:qwen3.7-max"},
	}
	if got := suggestPhysicalName(routes); got != "qwen-3.7-max" {
		t.Fatalf("suggestion=%q want qwen-3.7-max", got)
	}
}

func TestSuggestPhysicalNameDoesNotMergeDifferentRevisions(t *testing.T) {
	routes := []discoveredRoute{
		{ProviderPrefix: "orca", ExternalID: "deepseek-v4-flash-free"},
		{ProviderPrefix: "openrouter", ExternalID: "deepseek-v4-flash-0731:free"},
	}
	if got := suggestPhysicalName(routes); got != "" {
		t.Fatalf("different upstream revisions received an automatic group suggestion %q", got)
	}
}

func TestSuggestPhysicalNameHandlesSingleProviderRoute(t *testing.T) {
	routes := []discoveredRoute{{ExternalID: "vendor/gpt5.6-sol"}}
	if got := suggestPhysicalName(routes); got != "gpt-5.6-sol" {
		t.Fatalf("suggestion=%q want gpt-5.6-sol", got)
	}
}

func TestSuggestPhysicalNameRemovesMixedCaseProviderNamespace(t *testing.T) {
	routes := []discoveredRoute{
		{ProviderPrefix: "cbai", ExternalID: "glm-5.2"},
		{ProviderPrefix: "ocg", ExternalID: "glm-5.2"},
		{ProviderPrefix: "g4f", ExternalID: "AnyProvider:glm-5.2"},
	}
	if got := suggestPhysicalName(routes); got != "glm-5.2" {
		t.Fatalf("suggestion=%q want glm-5.2", got)
	}
}

func TestMatchingDiscoveredRoutesUsesCanonicalIdentityAndStableOrder(t *testing.T) {
	routes := []discoveredRoute{
		{ID: "z", ProviderPrefix: "z-provider", ExternalID: "qwen3.7-max"},
		{ID: "other", ProviderPrefix: "other", ExternalID: "qwen3.7-max-2025"},
		{ID: "x", ProviderPrefix: "x-provider", ExternalID: "qwen/qwen3.7-max:free"},
		{ID: "x", ProviderPrefix: "x-provider", ExternalID: "qwen3.7-max"},
	}
	got := matchingDiscoveredRoutes(routes, "qwen-3.7-max")
	if len(got) != 2 || got[0].ID != "x" || got[1].ID != "z" {
		t.Fatalf("matching discovered routes=%#v; want only equivalent routes in stable provider order", got)
	}
	if got := matchingDiscoveredRoutes(routes, ""); got != nil {
		t.Fatalf("empty canonical name matched routes: %#v", got)
	}
}
