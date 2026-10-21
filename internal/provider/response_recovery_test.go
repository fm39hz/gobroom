package provider

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHTTPJSONResponseContinuityClassification(t *testing.T) {
	classifier := HTTPJSONErrorClassifier{Options: &HTTPJSONErrorClassifierOptions{ResponseContinuityResetSignals: []HTTPJSONErrorSignal{{Codes: []string{"previous_response_not_found"}}}}}
	body := []byte(`{"error":{"code":"previous_response_not_found"}}`)
	got := classifier.ClassifyResponseError(body)
	if got.Class != kernel.ErrorTerminal || got.Cause != kernel.CauseContinuityInvalid || got.Effect != kernel.EffectAccepted || got.Scope != kernel.ScopeRequest || got.Retry != kernel.RetryNever || got.Recovery != kernel.CredentialRecoveryNone || got.ContinuityRecovery != kernel.ContinuityRecoveryInvalidate || got.StatusCode != 0 {
		t.Fatalf("response continuity outcome=%#v", got)
	}
	defaultOutcome := (HTTPJSONErrorClassifier{}).ClassifyResponseError(body)
	if defaultOutcome.Class != kernel.ErrorCooldown || defaultOutcome.Cause != kernel.CauseStreamFailure || defaultOutcome.Effect != kernel.EffectAccepted || defaultOutcome.Scope != kernel.ScopeOperation || defaultOutcome.Retry != kernel.RetryAfter || defaultOutcome.StatusCode != 0 {
		t.Fatalf("default response failure outcome=%#v", defaultOutcome)
	}
	if outcome := classifier.ClassifyOutcome(http.StatusBadRequest, nil, body); outcome.ContinuityRecovery == kernel.ContinuityRecoveryInvalidate {
		t.Fatalf("response-only signal affected HTTP error classification: %#v", outcome)
	}
	if outcome := classifier.ClassifyResponseError([]byte(`{"code":"previous_response_not_found"}`)); outcome.ContinuityRecovery == kernel.ContinuityRecoveryInvalidate {
		t.Fatalf("non-canonical body matched response signal: %#v", outcome)
	}
	registry, err := NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewRuntimeBindingBuilder(registry).Build(ProviderDefinitionRef("openai-compatible-responses", 1), OperationResponses)
	if err != nil {
		t.Fatal(err)
	}
	rich, ok := binding.ErrorClassifier.(kernel.ResponseOutcomeClassifier)
	if !ok {
		t.Fatal("Responses binding has no response outcome classifier")
	}
	if outcome := rich.ClassifyResponseError(body); outcome.ContinuityRecovery != kernel.ContinuityRecoveryInvalidate {
		t.Fatalf("builtin response signal did not match: %#v", outcome)
	}
	for _, status := range []int{400, 409} {
		outcome := binding.ErrorClassifier.(kernel.OutcomeClassifier).ClassifyOutcome(status, nil, body)
		if outcome.ContinuityRecovery != kernel.ContinuityRecoveryInvalidate {
			t.Fatalf("builtin HTTP status %d signal did not match: %#v", status, outcome)
		}
	}
}

func TestHTTPJSONResponseContinuityValidationCloneAndBinding(t *testing.T) {
	for _, invalid := range []HTTPJSONErrorSignal{
		{HTTPStatuses: []int{400}, Codes: []string{"x"}},
		{HTTPStatuses: []int{}, Codes: []string{"x"}},
		{},
		{Fields: []HTTPJSONFieldMatch{{Path: "/error/code"}}},
	} {
		if err := (HTTPJSONErrorClassifierOptions{ResponseContinuityResetSignals: []HTTPJSONErrorSignal{invalid}}).Validate(); err == nil {
			t.Errorf("invalid response signal accepted: %#v", invalid)
		}
	}
	signal := HTTPJSONErrorSignal{Codes: []string{"previous_response_not_found"}, Fields: []HTTPJSONFieldMatch{{Path: "/error/code", Equals: []string{"previous_response_not_found"}}}}
	configured, err := (HTTPJSONErrorClassifier{}).WithOptions(ErrorClassifierOptions{HTTPJSON: &HTTPJSONErrorClassifierOptions{ResponseContinuityResetSignals: []HTTPJSONErrorSignal{signal}}})
	if err != nil {
		t.Fatal(err)
	}
	signal.Codes[0] = "mutated"
	signal.Fields[0].Equals[0] = "mutated"
	cloned := configured.(HTTPJSONErrorClassifier).Options.ResponseContinuityResetSignals[0]
	if cloned.Codes[0] != "previous_response_not_found" || cloned.Fields[0].Equals[0] != "previous_response_not_found" {
		t.Fatalf("response signal was not deeply cloned: %#v", cloned)
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
		json.RawMessage(`{"httpJson":{"responseContinuityResetSignals":[{"codes":["previous_response_not_found"]}]}}`),
		json.RawMessage(`{"httpJson":{"responseContinuityResetSignals":[{"fields":[{"path":"/error/code","equals":["previous_response_not_found"]}] }]}}`),
	} {
		if err := snapshot.ValidateOptions(ref, raw); err != nil {
			t.Fatalf("registered schema rejected response signal: %v", err)
		}
		if _, _, err := snapshot.Bind(ref, raw); err != nil {
			t.Fatalf("registered binding rejected response signal: %v", err)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"httpJson":{"responseContinuityResetSignals":[{"httpStatuses":[400],"codes":["x"]}]}}`),
		json.RawMessage(`{"httpJson":{"responseContinuityResetSignals":[{"codes":[]}]}}`),
		json.RawMessage(`{"httpJson":{"responseContinuityResetSignals":[{"fields":[{"path":"bad","equals":["x"]}] }]}}`),
	} {
		if err := snapshot.ValidateOptions(ref, raw); err == nil {
			t.Errorf("registered schema accepted invalid response signal: %s", raw)
		}
	}
}

type responseClassifierFixture struct{}

func (responseClassifierFixture) ID() string                             { return "fixture" }
func (responseClassifierFixture) Classify(int, []byte) kernel.ErrorClass { return kernel.ErrorTerminal }
func (responseClassifierFixture) ClassifyResponseError([]byte) kernel.ClassifiedOutcome {
	return kernel.ClassifiedOutcome{Cause: kernel.CauseContinuityInvalid}
}

type richResponseClassifierFixture struct{ responseClassifierFixture }

func (richResponseClassifierFixture) ID() string { return "fixture-rich" }
func (richResponseClassifierFixture) ClassifyOutcome(int, http.Header, []byte) kernel.ClassifiedOutcome {
	return kernel.ClassifiedOutcome{Cause: kernel.CauseSuccess}
}

func TestAdaptErrorClassifierPreservesResponseCapability(t *testing.T) {
	adapted := adaptErrorClassifier(HTTPJSONErrorClassifier{})
	if _, ok := adapted.(kernel.ResponseOutcomeClassifier); !ok {
		t.Fatal("built-in classifier response capability was lost")
	}
	for _, classifier := range []ErrorClassifier{responseClassifierFixture{}, richResponseClassifierFixture{}} {
		got := adaptErrorClassifier(classifier)
		if _, ok := got.(kernel.ResponseOutcomeClassifier); !ok {
			t.Fatalf("custom classifier %T lost response capability", classifier)
		}
		_, hasRich := got.(kernel.OutcomeClassifier)
		if hasRich != (classifier.ID() == "fixture-rich") {
			t.Fatalf("custom classifier %T rich capability=%v", classifier, hasRich)
		}
	}
}
