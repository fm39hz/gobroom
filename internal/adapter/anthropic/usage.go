package anthropic

import (
	"encoding/json"
	"errors"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func (usage *anthropicUsageDTO) UnmarshalJSON(data []byte) error {
	var decoded struct {
		InputTokens         usagewire.Counter `json:"input_tokens"`
		OutputTokens        usagewire.Counter `json:"output_tokens"`
		CachedReadTokens    usagewire.Counter `json:"cache_read_input_tokens"`
		CachedWriteTokens   usagewire.Counter `json:"cache_creation_input_tokens"`
		OutputTokensDetails json.RawMessage   `json:"output_tokens_details"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			*usage = anthropicUsageDTO{AccountingIssues: []kernel.UsageIssue{{Code: "invalid_usage_counter"}}}
			return nil
		}
		return err
	}
	*usage = anthropicUsageDTO{
		InputTokens: decoded.InputTokens, OutputTokens: decoded.OutputTokens,
		CachedReadTokens: decoded.CachedReadTokens, CachedWriteTokens: decoded.CachedWriteTokens,
	}
	if len(decoded.OutputTokensDetails) > 0 && string(decoded.OutputTokensDetails) != "null" {
		var details anthropicOutputTokensDetails
		if err := json.Unmarshal(decoded.OutputTokensDetails, &details); err != nil {
			usage.OutputTokensDetails = &anthropicOutputTokensDetails{
				ThinkingTokens: usagewire.Counter{Invalid: true, Present: true},
			}
			usage.AccountingIssues = []kernel.UsageIssue{{Code: "invalid_usage_counter"}}
		} else {
			usage.OutputTokensDetails = &details
		}
	}
	if usage.InputTokens.Invalid || usage.OutputTokens.Invalid || usage.CachedReadTokens.Invalid || usage.CachedWriteTokens.Invalid ||
		usage.OutputTokensDetails != nil && usage.OutputTokensDetails.ThinkingTokens.Invalid {
		usage.AccountingIssues = []kernel.UsageIssue{{Code: "invalid_usage_counter"}}
	}
	return nil
}

func decodeAnthropicUsage(data json.RawMessage) (kernel.UsageEvent, error) {
	var native anthropicUsageDTO
	if err := json.Unmarshal(data, &native); err != nil {
		return kernel.UsageEvent{}, err
	}
	return (anthropicUsageState{}).apply(&native).snapshot(), nil
}
