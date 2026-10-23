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
	InputDetails      openAIUsageDetails `json:"input_tokens_details"`
	PromptDetails     openAIUsageDetails `json:"prompt_tokens_details"`
	OutputDetails     openAIUsageDetails `json:"output_tokens_details"`
	CompletionDetails openAIUsageDetails `json:"completion_tokens_details"`
	Cost              json.RawMessage    `json:"cost"`
	Currency          json.RawMessage    `json:"currency"`
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
		Status: "ok", UpdateKind: kernel.UsageFullSnapshot}
	if state.charge != nil {
		event.Extensions = append(event.Extensions, state.charge...)
	}
	return event.Clone()
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
