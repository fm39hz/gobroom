package openai

import (
	"bytes"
	"encoding/json"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

type openAIUsageDetails struct {
	Cached           usagewire.Counter `json:"cached_tokens"`
	Write            usagewire.Counter `json:"cache_write_tokens"`
	Reasoning        usagewire.Counter `json:"reasoning_tokens"`
	Present, Invalid bool              `json:"-"`
}

func (details *openAIUsageDetails) UnmarshalJSON(raw []byte) error {
	type fields openAIUsageDetails
	var value fields
	if err := json.Unmarshal(raw, &value); err != nil {
		if !json.Valid(raw) {
			return err
		}
		*details = openAIUsageDetails{Present: true, Invalid: true}
		return nil
	}
	*details = openAIUsageDetails(value)
	details.Present = true
	return nil
}

type openAIUsageWire struct {
	Input             usagewire.Counter  `json:"input_tokens"`
	Output            usagewire.Counter  `json:"output_tokens"`
	Prompt            usagewire.Counter  `json:"prompt_tokens"`
	Completion        usagewire.Counter  `json:"completion_tokens"`
	Total             usagewire.Counter  `json:"total_tokens"`
	InputDetails      openAIUsageDetails `json:"input_tokens_details"`
	PromptDetails     openAIUsageDetails `json:"prompt_tokens_details"`
	OutputDetails     openAIUsageDetails `json:"output_tokens_details"`
	CompletionDetails openAIUsageDetails `json:"completion_tokens_details"`
	Cost              json.RawMessage    `json:"cost"`
	Currency          json.RawMessage    `json:"currency"`
}

type openAIRawUsageDetails struct {
	Cached    usagewire.Counter `json:"cached_tokens"`
	Write     usagewire.Counter `json:"cache_write_tokens"`
	Reasoning usagewire.Counter `json:"reasoning_tokens"`
}

type openAIRawUsageShape struct {
	Input             usagewire.Counter     `json:"input_tokens"`
	Output            usagewire.Counter     `json:"output_tokens"`
	Prompt            usagewire.Counter     `json:"prompt_tokens"`
	Completion        usagewire.Counter     `json:"completion_tokens"`
	Total             usagewire.Counter     `json:"total_tokens"`
	InputDetails      openAIRawUsageDetails `json:"input_tokens_details"`
	PromptDetails     openAIRawUsageDetails `json:"prompt_tokens_details"`
	OutputDetails     openAIRawUsageDetails `json:"output_tokens_details"`
	CompletionDetails openAIRawUsageDetails `json:"completion_tokens_details"`
	Cost              json.RawMessage       `json:"cost"`
	Currency          json.RawMessage       `json:"currency"`
}

func captureOpenAIRawUsage(raw json.RawMessage, hooks kernel.StreamHooks, api string) (*kernel.RawUsageFragment, []kernel.UsageIssue) {
	if !hooks.UsageCapture.Enabled {
		return nil, nil
	}
	if hooks.UsageCapture.MaxBytes <= 0 || int64(len(raw)) > hooks.UsageCapture.MaxBytes {
		return nil, []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
	}
	var shape openAIRawUsageShape
	if usagewire.DecodeStrict(raw, &shape) != nil || !openAIRawUsageCountersValid(shape) || !openAIRawUsageChargeValid(shape) {
		return nil, []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
	}
	return kernel.SelectRawUsage(hooks.UsageCapture, hooks.ProviderIssuer, api, raw)
}

func openAIRawUsageCountersValid(shape openAIRawUsageShape) bool {
	return usagewire.CountersValid(shape.Input, shape.Output, shape.Prompt, shape.Completion, shape.Total,
		shape.InputDetails.Cached, shape.InputDetails.Write, shape.InputDetails.Reasoning,
		shape.PromptDetails.Cached, shape.PromptDetails.Write, shape.PromptDetails.Reasoning,
		shape.OutputDetails.Cached, shape.OutputDetails.Write, shape.OutputDetails.Reasoning,
		shape.CompletionDetails.Cached, shape.CompletionDetails.Write, shape.CompletionDetails.Reasoning)
}

func openAIRawUsageChargeValid(shape openAIRawUsageShape) bool {
	if len(shape.Cost) != 0 && !bytes.Equal(bytes.TrimSpace(shape.Cost), []byte("null")) {
		var currency *string
		if err := json.Unmarshal(currencyJSON(shape.Currency), &currency); err != nil {
			return false
		}
		_, err := usagewire.ChargeValue(shape.Cost, currency, "body", "/usage/cost")
		return err == nil
	}
	if len(shape.Currency) == 0 || bytes.Equal(bytes.TrimSpace(shape.Currency), []byte("null")) {
		return true
	}
	var currency string
	if err := json.Unmarshal(shape.Currency, &currency); err != nil {
		return false
	}
	_, err := usagewire.ChargeValue(json.RawMessage(`0`), &currency, "body", "/usage/cost")
	return err == nil
}

func decodeOpenAIUsage(raw json.RawMessage) (kernel.UsageEvent, error) {
	wire, err := decodeOpenAIUsageWire(raw)
	if err != nil {
		if !json.Valid(raw) {
			return kernel.UsageEvent{}, err
		}
		return kernel.UsageEvent{AccountingIssues: []kernel.UsageIssue{{Code: "invalid_usage_counter"}}}, nil
	}
	return (openAIUsageState{}).apply(&wire).snapshot(), nil
}

func decodeOpenAIUsageWire(raw json.RawMessage) (openAIUsageWire, error) {
	var wire openAIUsageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return openAIUsageWire{}, err
	}
	return wire, nil
}

type openAIUsageState struct {
	input, output, cached, written, reasoning *int64
	charge                                    []extensions.UsageValue
	issues                                    []kernel.UsageIssue
	rawUsage                                  *kernel.RawUsageFragment
}

func (state openAIUsageState) apply(wire *openAIUsageWire) openAIUsageState {
	if wire == nil {
		return state
	}
	input, output := wire.Input, wire.Output
	if !input.Present {
		input = wire.Prompt
	}
	if !output.Present {
		output = wire.Completion
	}
	inDetails, outDetails := wire.InputDetails, wire.OutputDetails
	if !inDetails.Present {
		inDetails = wire.PromptDetails
	}
	if !outDetails.Present {
		outDetails = wire.CompletionDetails
	}
	applyCounter := func(counter usagewire.Counter, target **int64) {
		if counter.Present {
			if counter.Invalid {
				*target = nil
			} else {
				*target = copyOpenAICount(counter.Value)
			}
		}
	}
	applyCounter(input, &state.input)
	applyCounter(output, &state.output)
	applyCounter(inDetails.Cached, &state.cached)
	applyCounter(inDetails.Write, &state.written)
	applyCounter(outDetails.Reasoning, &state.reasoning)
	if inDetails.Invalid {
		state.cached, state.written = nil, nil
	}
	if outDetails.Invalid {
		state.reasoning = nil
	}
	if input.Invalid || output.Invalid || inDetails.Invalid || outDetails.Invalid || inDetails.Cached.Invalid || inDetails.Write.Invalid || outDetails.Reasoning.Invalid {
		state.issues = appendIssue(state.issues, "invalid_usage_counter")
	}
	if len(wire.Cost) > 0 {
		state.charge = nil
	}
	if len(wire.Cost) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Cost), []byte("null")) {
		var currency *string
		if err := json.Unmarshal(currencyJSON(wire.Currency), &currency); err != nil {
			state.issues = appendIssue(state.issues, "invalid_reported_charge")
			return state
		}
		value, err := usagewire.ChargeValue(wire.Cost, currency, "body", "/usage/cost")
		if err != nil {
			state.issues = appendIssue(state.issues, "invalid_reported_charge")
		} else {
			state.charge = []extensions.UsageValue{value}
		}
	}
	return state
}

func (state openAIUsageState) snapshot() kernel.UsageEvent {
	event := kernel.UsageEvent{InputTokens: copyOpenAICount(state.input), OutputTokens: copyOpenAICount(state.output),
		CachedReadTokens: copyOpenAICount(state.cached), CachedWriteTokens: copyOpenAICount(state.written),
		ReasoningTokens: copyOpenAICount(state.reasoning), AccountingIssues: append([]kernel.UsageIssue(nil), state.issues...),
		Status: "ok", UpdateKind: kernel.UsageFullSnapshot, RawUsage: cloneOpenAIRawUsage(state.rawUsage)}
	if state.charge != nil {
		event.Extensions = append(event.Extensions, state.charge...)
	}
	return event.Clone()
}

func cloneOpenAIRawUsage(raw *kernel.RawUsageFragment) *kernel.RawUsageFragment {
	if raw == nil {
		return nil
	}
	copy := *raw
	copy.Value = append(json.RawMessage(nil), raw.Value...)
	return &copy
}

func copyOpenAICount(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func appendIssue(issues []kernel.UsageIssue, code string) []kernel.UsageIssue {
	for _, issue := range issues {
		if issue.Code == code {
			return issues
		}
	}
	return append(issues, kernel.UsageIssue{Code: code})
}

func currencyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`null`)
	}
	return raw
}
