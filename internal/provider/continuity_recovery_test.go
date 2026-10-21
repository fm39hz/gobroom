package provider

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHTTPJSONContinuityResetForcesInvalidationOutcome(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{
		DefaultScope:      kernel.ScopeProvider,
		QuotaHTTPStatuses: []int{422}, QuotaCodes: []string{"continuity_invalid"},
		ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{422}, Codes: []string{"continuity_invalid"}}},
	}}
	body := []byte(`{"error":{"code":"continuity_invalid"}}`)
	if class := classifier.Classify(422, body); class != kernel.ErrorTerminal {
		t.Fatalf("continuity class=%q", class)
	}
	got := classifier.ClassifyOutcome(422, http.Header{"Retry-After": []string{"3600"}}, body)
	if got.Class != kernel.ErrorTerminal || got.Cause != kernel.CauseContinuityInvalid || got.Scope != kernel.ScopeRequest || got.Retry != kernel.RetryNever || got.Recovery != kernel.CredentialRecoveryNone || got.Effect != kernel.EffectRejected || got.ContinuityRecovery != kernel.ContinuityRecoveryInvalidate {
		t.Fatalf("continuity outcome=%#v", got)
	}
	for _, status := range []int{200, 503} {
		if outcome := classifier.ClassifyOutcome(status, nil, body); outcome.ContinuityRecovery == kernel.ContinuityRecoveryInvalidate {
			t.Fatalf("status %d unexpectedly invalidated continuity: %#v", status, outcome)
		}
	}
}

func TestBuiltinResponsesDeclaresStructuredContinuationReset(t *testing.T) {
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(ProviderDefinitionRef("openai-compatible-responses", 1), OperationResponses)
	if err != nil {
		t.Fatal(err)
	}
	classifier, ok := binding.ErrorClassifier.(kernel.OutcomeClassifier)
	if !ok {
		t.Fatal("Responses binding has no typed outcome classifier")
	}
	for _, status := range []int{400, 409} {
		outcome := classifier.ClassifyOutcome(status, nil, []byte(`{"error":{"code":"previous_response_not_found","type":"invalid_request_error"}}`))
		if outcome.ContinuityRecovery != kernel.ContinuityRecoveryInvalidate || outcome.Cause != kernel.CauseContinuityInvalid || outcome.Retry != kernel.RetryNever {
			t.Fatalf("status=%d outcome=%#v", status, outcome)
		}
	}
}

func TestHTTPJSONContinuityResetValidation(t *testing.T) {
	cases := []HTTPJSONErrorClassifierOptions{
		{ContinuityResetSignals: []HTTPJSONErrorSignal{{Codes: []string{"x"}}}},
		{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{422}}}},
		{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{401}, Codes: []string{"x"}}}},
		{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{408}, Codes: []string{"x"}}}},
		{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{429}, Codes: []string{"x"}}}},
		{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{500}, Codes: []string{"x"}}}},
	}
	for _, options := range cases {
		if err := options.Validate(); err == nil {
			t.Errorf("invalid continuity options accepted: %#v", options)
		}
	}
	if err := (HTTPJSONErrorClassifierOptions{ContinuityResetSignals: []HTTPJSONErrorSignal{{HTTPStatuses: []int{422}, Fields: []HTTPJSONFieldMatch{{Path: "/error/reason", Equals: []string{"reset"}}}}}}).Validate(); err != nil {
		t.Fatalf("valid structured continuity signal rejected: %v", err)
	}
}

func TestHTTPJSONContinuityResetCloneAndSchema(t *testing.T) {
	signal := HTTPJSONErrorSignal{HTTPStatuses: []int{422}, Codes: []string{"invalid"}, Fields: []HTTPJSONFieldMatch{{Path: "/error/code", Equals: []string{"invalid"}}}}
	configured, err := (HTTPJSONErrorClassifier{}).WithOptions(ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{ContinuityResetSignals: []HTTPJSONErrorSignal{signal}}})
	if err != nil {
		t.Fatal(err)
	}
	options := configured.(HTTPJSONErrorClassifier).Options
	signal.HTTPStatuses[0] = 400
	signal.Codes[0] = "changed"
	signal.Fields[0].Equals[0] = "changed"
	cloned := options.ContinuityResetSignals[0]
	if cloned.HTTPStatuses[0] != 422 || cloned.Codes[0] != "invalid" || cloned.Fields[0].Equals[0] != "invalid" {
		t.Fatalf("continuity signal was not deeply cloned: %#v", cloned)
	}
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ref := extensionRef(PrimitiveRef{Kind: PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1})
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"httpJson":{"continuityResetSignals":[{"httpStatuses":[400,409],"codes":["previous_response_not_found"]}]}}`),
		json.RawMessage(`{"httpJson":{"continuityResetSignals":[{"httpStatuses":[422],"fields":[{"path":"/error/reason","equals":["expired"]}]}]}}`),
	} {
		if err := snapshot.ValidateOptions(ref, raw); err != nil {
			t.Fatalf("registered schema rejected valid continuity options: %v", err)
		}
		if _, _, err := snapshot.Bind(ref, raw); err != nil {
			t.Fatalf("registered factory rejected continuity options: %v", err)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"httpJson":{"continuityResetSignals":[{"codes":["missing_status"]}]}}`),
		json.RawMessage(`{"httpJson":{"continuityResetSignals":[{"httpStatuses":[400]}]}}`),
		json.RawMessage(`{"httpJson":{"continuityResetSignals":[{"httpStatuses":[500],"codes":["unsafe"]}]}}`),
		json.RawMessage(`{"httpJson":{"continuityResetSignals":[{"httpStatuses":[400],"codes":["x"],"unknown":true}]}}`),
	} {
		if err := snapshot.ValidateOptions(ref, raw); err == nil {
			t.Fatalf("registered schema accepted invalid continuity options: %s", raw)
		}
	}
}
