package gemini

import (
	"encoding/json"

	"github.com/fm39hz/gobroom/internal/kernel"
)

// UnmarshalJSON only relaxes optional usage metadata. The enclosing response,
// content, and tool fields still use their normal JSON decoders.
func (usage *geminiUsageMetadata) UnmarshalJSON(data []byte) error {
	*usage = geminiUsageMetadata{}
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
		{"promptTokenCount", &usage.PromptTokenCount},
		{"candidatesTokenCount", &usage.CandidatesTokenCount},
		{"totalTokenCount", &usage.TotalTokenCount},
		{"cachedContentTokenCount", &usage.CachedContentTokenCount},
		{"thoughtsTokenCount", &usage.ThoughtsTokenCount},
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
