package gemini

import (
	"encoding/json"
	"errors"

	"github.com/fm39hz/gobroom/internal/kernel"
)

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
