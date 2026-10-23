package gemini

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/kernel"
)

type geminiRawUsageShape struct {
	Prompt     usagewire.Counter `json:"promptTokenCount"`
	Candidates usagewire.Counter `json:"candidatesTokenCount"`
	Total      usagewire.Counter `json:"totalTokenCount"`
	Cached     usagewire.Counter `json:"cachedContentTokenCount"`
	Thoughts   usagewire.Counter `json:"thoughtsTokenCount"`
}

func captureGeminiRawUsage(raw json.RawMessage, hooks kernel.StreamHooks) (*kernel.RawUsageFragment, []kernel.UsageIssue) {
	if !hooks.UsageCapture.Enabled {
		return nil, nil
	}
	if hooks.UsageCapture.MaxBytes <= 0 || int64(len(raw)) > hooks.UsageCapture.MaxBytes {
		return nil, []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
	}
	var shape geminiRawUsageShape
	if usagewire.DecodeStrict(raw, &shape) != nil || !usagewire.CountersValid(shape.Prompt, shape.Candidates, shape.Total, shape.Cached, shape.Thoughts) {
		return nil, []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
	}
	return kernel.SelectRawUsage(hooks.UsageCapture, hooks.ProviderIssuer, "gemini.generate-content", raw)
}

type geminiUsageCaptureEnvelope struct {
	UsageMetadata usagewire.RawSlot `json:"usageMetadata"`
}

func captureGeminiUsagePayload(payloadRaw []byte, payload *geminiGenerateContentResponse, hooks kernel.StreamHooks) {
	if !hooks.UsageCapture.Enabled || payload == nil || payload.UsageMetadata == nil {
		return
	}
	var envelope geminiUsageCaptureEnvelope
	envelope.UsageMetadata.MaxBytes = hooks.UsageCapture.MaxBytes
	if err := jsonv2.Unmarshal(payloadRaw, &envelope); err != nil {
		payload.UsageMetadata.RawUsageChecked = true
		payload.UsageMetadata.RawUsage = nil
		payload.UsageMetadata.RawUsageIssues = []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
		return
	}
	if !envelope.UsageMetadata.Present {
		return
	}
	payload.UsageMetadata.RawUsageChecked = true
	if envelope.UsageMetadata.Rejected {
		payload.UsageMetadata.RawUsage = nil
		payload.UsageMetadata.RawUsageIssues = []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
		return
	}
	payload.UsageMetadata.RawUsage, payload.UsageMetadata.RawUsageIssues = captureGeminiRawUsage(envelope.UsageMetadata.Value, hooks)
}

func (usage *geminiUsageMetadata) UnmarshalJSON(data []byte) error {
	type wire geminiUsageMetadata
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			*usage = geminiUsageMetadata{AccountingIssues: []kernel.UsageIssue{{Code: "invalid_usage_counter"}}}
			return nil
		}
		return err
	}
	*usage = geminiUsageMetadata(decoded)
	if usage.PromptTokenCount.Invalid || usage.CandidatesTokenCount.Invalid || usage.TotalTokenCount.Invalid ||
		usage.CachedContentTokenCount.Invalid || usage.ThoughtsTokenCount.Invalid {
		usage.AccountingIssues = []kernel.UsageIssue{{Code: "invalid_usage_counter"}}
	}
	return nil
}

func decodeGeminiUsage(data json.RawMessage) (kernel.UsageEvent, error) {
	var native geminiUsageMetadata
	if err := json.Unmarshal(data, &native); err != nil {
		return kernel.UsageEvent{}, err
	}
	return geminiUsage(&native), nil
}
