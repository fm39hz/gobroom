package kernel

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestSelectRawUsageIsOptInBoundedAndPrivate(t *testing.T) {
	provider := extensions.Ref{Kind: "provider-definition", ID: "fixture-provider", ContractVersion: 1}
	value := json.RawMessage(`{"input_tokens":9007199254740993,"private":"fixture-secret-usage-value"}`)

	fragment, issues := SelectRawUsage(UsageCapturePolicy{Enabled: false, MaxBytes: 1024}, provider, "chat", value)
	if fragment != nil || len(issues) != 0 {
		t.Fatalf("disabled raw capture retained data or emitted an issue: fragment=%#v issues=%#v", fragment, issues)
	}

	fragment, issues = SelectRawUsage(UsageCapturePolicy{Enabled: true, MaxBytes: 1024}, provider, "chat", value)
	if fragment == nil || len(issues) != 0 {
		t.Fatalf("enabled capture failed for a bounded usage object: fragment=%#v issues=%#v", fragment, issues)
	}
	if !bytes.Equal(fragment.Value, value) {
		t.Fatalf("captured JSON changed: got %s want %s", fragment.Value, value)
	}
	if !bytes.Contains(fragment.Value, []byte("9007199254740993")) {
		t.Fatalf("captured number lost precision: %s", fragment.Value)
	}
	if fragment.ProviderDefinition != provider || fragment.API != "chat" {
		t.Fatalf("capture identity/API mismatch: %#v", fragment)
	}
	apiNamedLikePrivateData := "private-fixture-secret-invalid-input"
	accepted, acceptedIssues := SelectRawUsage(UsageCapturePolicy{Enabled: true, MaxBytes: 1024}, provider, apiNamedLikePrivateData, json.RawMessage(`{"usage":1}`))
	if accepted == nil || len(acceptedIssues) != 0 {
		t.Fatalf("ordinary API identifier was rejected by a name heuristic: fragment=%#v issues=%#v", accepted, acceptedIssues)
	}
	defaultPolicy := DefaultUsageCapturePolicy()
	if defaultPolicy.Enabled || defaultPolicy.MaxBytes != 262144 {
		t.Fatalf("unexpected default raw capture policy: %#v", defaultPolicy)
	}
	fragment.Value[0] = '['
	if !bytes.Equal(value, json.RawMessage(`{"input_tokens":9007199254740993,"private":"fixture-secret-usage-value"}`)) {
		t.Fatal("capture shares the caller's raw JSON buffer")
	}

	for name, input := range map[string]json.RawMessage{
		"oversized valid object": json.RawMessage(`{"usage":"fixture-secret-oversized"}`),
		"truncated object":       json.RawMessage(`{"usage":"fixture-secret-truncated"}`),
	} {
		t.Run(name, func(t *testing.T) {
			limit := int64(len(input) - 1)
			got, gotIssues := SelectRawUsage(UsageCapturePolicy{Enabled: true, MaxBytes: limit}, provider, "chat", input)
			if got != nil || len(gotIssues) == 0 {
				t.Fatalf("unsafe oversized input must be omitted with an issue: fragment=%#v issues=%#v", got, gotIssues)
			}
			if bytes.Contains([]byte(jsonIssueText(gotIssues)), []byte("fixture-secret")) {
				t.Fatalf("issue leaked payload text: %#v", gotIssues)
			}
		})
	}
}

func TestSelectRawUsageRejectsInvalidInputsWithConstantIssues(t *testing.T) {
	validProvider := extensions.Ref{Kind: "provider-definition", ID: "fixture-provider", ContractVersion: 1}
	cases := map[string]struct {
		provider extensions.Ref
		api      string
		value    json.RawMessage
	}{
		"invalid issuer":  {provider: extensions.Ref{Kind: "provider-definition", ID: "fixture-provider\ninvalid", ContractVersion: 1}, api: "chat", value: json.RawMessage(`{"usage":1}`)},
		"empty API":       {provider: validProvider, api: "", value: json.RawMessage(`{"usage":1}`)},
		"control API":     {provider: validProvider, api: "chat\ninvalid", value: json.RawMessage(`{"usage":1}`)},
		"non-object JSON": {provider: validProvider, api: "chat", value: json.RawMessage(`"private-fixture-secret-invalid-input"`)},
		"malformed JSON":  {provider: validProvider, api: "chat", value: json.RawMessage(`{"private":"fixture-secret-invalid-input"`)},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			fragment, issues := SelectRawUsage(UsageCapturePolicy{Enabled: true, MaxBytes: 1024}, input.provider, input.api, input.value)
			if fragment != nil || len(issues) == 0 {
				t.Fatalf("invalid input must be omitted with an issue: fragment=%#v issues=%#v", fragment, issues)
			}
			if len(issues) != 1 || issues[0] != (UsageIssue{Code: invalidRawUsageCode}) ||
				bytes.Contains([]byte(jsonIssueText(issues)), []byte("fixture-secret")) {
				t.Fatalf("issue leaked issuer, API, or payload data: %#v", issues)
			}
		})
	}
}

func jsonIssueText(issues []UsageIssue) string {
	encoded, err := json.Marshal(issues)
	if err != nil {
		return err.Error()
	}
	return string(encoded)
}

func TestUsageRawCaptureCloneMergeOwnsBytesAndNilCompletionPreservesFragment(t *testing.T) {
	captured := json.RawMessage(`{"input_tokens":9007199254740993}`)
	original := UsageEvent{RawUsage: &RawUsageFragment{
		ProviderDefinition: extensions.Ref{Kind: "provider-definition", ID: "fixture-provider", ContractVersion: 1},
		API:                "chat",
		Value:              captured,
	}}

	clone := original.Clone()
	clone.RawUsage.Value[0] = '['
	if !bytes.Equal(original.RawUsage.Value, captured) {
		t.Fatal("UsageEvent.Clone shares RawUsage.Value with its source")
	}

	merged := MergeProviderUsage(original, UsageEvent{})
	if merged.RawUsage == nil || !bytes.Equal(merged.RawUsage.Value, captured) {
		t.Fatalf("nil completion discarded the last captured fragment: %#v", merged.RawUsage)
	}
	merged.RawUsage.Value[0] = '['
	if !bytes.Equal(original.RawUsage.Value, captured) {
		t.Fatal("MergeProviderUsage shares RawUsage.Value with its source")
	}

	updateBytes := json.RawMessage(`{"output_tokens":7}`)
	update := UsageEvent{RawUsage: &RawUsageFragment{
		ProviderDefinition: original.RawUsage.ProviderDefinition,
		API:                "chat",
		Value:              updateBytes,
	}}
	merged = MergeProviderUsage(original, update)
	merged.RawUsage.Value[0] = '['
	if !bytes.Equal(updateBytes, json.RawMessage(`{"output_tokens":7}`)) {
		t.Fatal("MergeProviderUsage shares a newly captured fragment with the update")
	}
}
