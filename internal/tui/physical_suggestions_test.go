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
