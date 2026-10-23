package anthropic

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"github.com/fm39hz/gobroom/internal/kernel"
)

type anthropicRawUsageShape struct {
	InputTokens       usagewire.Counter                    `json:"input_tokens"`
	OutputTokens      usagewire.Counter                    `json:"output_tokens"`
	CachedReadTokens  usagewire.Counter                    `json:"cache_read_input_tokens"`
	CachedWriteTokens usagewire.Counter                    `json:"cache_creation_input_tokens"`
	OutputDetails     anthropicRawOutputTokensDetailsShape `json:"output_tokens_details"`
}

type anthropicRawOutputTokensDetailsShape struct {
	ThinkingTokens usagewire.Counter `json:"thinking_tokens"`
}

func captureAnthropicRawUsage(raw json.RawMessage, hooks kernel.StreamHooks) (*kernel.RawUsageFragment, []kernel.UsageIssue) {
	if !hooks.UsageCapture.Enabled {
		return nil, nil
	}
	if hooks.UsageCapture.MaxBytes <= 0 || int64(len(raw)) > hooks.UsageCapture.MaxBytes {
		return nil, []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
	}
	var shape anthropicRawUsageShape
	if usagewire.DecodeStrict(raw, &shape) != nil || !usagewire.CountersValid(
		shape.InputTokens, shape.OutputTokens, shape.CachedReadTokens,
		shape.CachedWriteTokens, shape.OutputDetails.ThinkingTokens,
	) {
		return nil, []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
	}
	return kernel.SelectRawUsage(hooks.UsageCapture, hooks.ProviderIssuer, "anthropic.messages", raw)
}

type anthropicUsageCaptureEnvelope struct {
	Message struct {
		Usage usagewire.RawSlot `json:"usage"`
	} `json:"message"`
	Usage usagewire.RawSlot `json:"usage"`
}

func captureAnthropicUsageFrame(frame *anthropicUsageFrame, payload []byte, hooks kernel.StreamHooks) {
	if !hooks.UsageCapture.Enabled {
		return
	}
	var envelope anthropicUsageCaptureEnvelope
	envelope.Message.Usage.MaxBytes = hooks.UsageCapture.MaxBytes
	envelope.Usage.MaxBytes = hooks.UsageCapture.MaxBytes
	if err := jsonv2.Unmarshal(payload, &envelope); err != nil {
		for _, usage := range []*anthropicUsageDTO{frame.MessageUsage, frame.EventUsage} {
			if usage != nil {
				usage.RawUsageChecked = true
				usage.RawUsage = nil
				usage.RawUsageIssues = []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
			}
		}
		return
	}
	for _, item := range []struct {
		usage **anthropicUsageDTO
		slot  *usagewire.RawSlot
	}{{&frame.MessageUsage, &envelope.Message.Usage}, {&frame.EventUsage, &envelope.Usage}} {
		if !item.slot.Present || !item.slot.Rejected && bytes.Equal(bytes.TrimSpace(item.slot.Value), []byte("null")) {
			continue
		}
		if *item.usage == nil {
			*item.usage = &anthropicUsageDTO{}
		}
		usage := *item.usage
		usage.RawUsageChecked = true
		if item.slot.Rejected {
			usage.RawUsage = nil
			usage.RawUsageIssues = []kernel.UsageIssue{{Code: usagewire.RawCaptureIssueCode}}
			continue
		}
		usage.RawUsage, usage.RawUsageIssues = captureAnthropicRawUsage(item.slot.Value, hooks)
	}
}

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
