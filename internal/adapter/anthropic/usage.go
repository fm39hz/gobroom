package anthropic

import (
	"encoding/json"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// UnmarshalJSON isolates optional accounting faults from response decoding.
// Each valid counter survives even when a sibling counter is invalid.
func (usage *anthropicUsageDTO) UnmarshalJSON(data []byte) error {
	*usage = anthropicUsageDTO{}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		if _, optionalFault := err.(*json.UnmarshalTypeError); !optionalFault {
			return err
		}
		usage.AccountingIssues = []kernel.UsageIssue{{Code: "invalid_usage_counter"}}
		return nil
	}
	invalid := false
	for _, field := range []struct {
		name        string
		destination **int64
	}{
		{"input_tokens", &usage.InputTokens},
		{"output_tokens", &usage.OutputTokens},
		{"cache_read_input_tokens", &usage.CachedReadTokens},
		{"cache_creation_input_tokens", &usage.CachedWriteTokens},
		{"reasoning_tokens", &usage.ReasoningTokens},
	} {
		raw, present := fields[field.name]
		if !present {
			continue
		}
		var count *int64
		if err := json.Unmarshal(raw, &count); err != nil || (count != nil && *count < 0) {
			invalid = true
			continue
		}
		*field.destination = count
	}
	if invalid {
		usage.AccountingIssues = []kernel.UsageIssue{{Code: "invalid_usage_counter"}}
	}
	return nil
}
