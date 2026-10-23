package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func emitOpenAIEvent(payload map[string]any, emit func(kernel.ResponseEvent)) error {
	if emit == nil {
		return nil
	}
	now := time.Now()
	objectType := stringValue(payload["object"])
	typ := stringValue(payload["type"])
	responseID := stringValue(payload["response_id"])
	itemID := stringValue(payload["item_id"])
	if responseID == "" && strings.HasPrefix(objectType, "chat.completion") {
		responseID = stringValue(payload["id"])
	}
	if responseID == "" && objectType == "response" {
		responseID = stringValue(payload["id"])
	}
	if responseID == "" {
		if response, ok := payload["response"].(map[string]any); ok {
			responseID = stringValue(response["id"])
		}
	}
	if responseID != "" {
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseStarted, ResponseID: responseID})
	}
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if len(delta) == 0 {
			delta, _ = choice["message"].(map[string]any)
		}
		if text, ok := delta["content"].(string); ok && text != "" {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, Text: text})
		}
		if text, ok := delta["reasoning_content"].(string); ok && text != "" {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, Text: text})
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			for index, raw := range calls {
				call, _ := raw.(map[string]any)
				function, _ := call["function"].(map[string]any)
				toolIndex := index
				if rawIndex, exists := call["index"]; exists {
					n, valid := integerValue(rawIndex)
					if !valid || n < 0 || uint64(n) > uint64(int(^uint(0)>>1)) {
						return fmt.Errorf("OpenAI tool call delta has invalid index %v", rawIndex)
					}
					toolIndex = int(n)
				}
				emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, Index: toolIndex, ToolCallID: stringValue(call["id"]), ToolName: stringValue(function["name"]), ToolArguments: stringValue(function["arguments"])})
			}
		}
		if finish := stringValue(choice["finish_reason"]); finish != "" {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, StopReason: finish})
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete})
		}
	}
	switch {
	case objectType == "response" && typ == "":
		if err := emitOpenAIResponsesJSON(payload, emit, now, responseID); err != nil {
			return err
		}
	case strings.Contains(typ, "output_item.added"):
		item, _ := payload["item"].(map[string]any)
		itemType := stringValue(payload["item_type"])
		if itemType == "" {
			itemType = stringValue(item["type"])
		}
		if itemID == "" {
			itemID = stringValue(item["id"])
		}
		callID := stringValue(item["call_id"])
		if callID == "" && itemType == "function_call" {
			callID = stringValue(item["id"])
		}
		toolName := stringValue(item["name"])
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockStart, ResponseID: responseID, ItemID: itemID, BlockType: itemType, ToolCallID: callID, ToolName: toolName})
		if itemType == "function_call" && (callID != "" || toolName != "") {
			emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, ResponseID: responseID, ItemID: itemID, ContentType: "function_call", ToolCallID: callID, ToolName: toolName, ToolArguments: stringValue(item["arguments"])})
		}
	case strings.Contains(typ, "output_item.done"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventContentBlockEnd, ResponseID: responseID, ItemID: itemID, StopReason: stringValue(payload["status"])})
	case strings.Contains(typ, "output_text.delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventTextDelta, ResponseID: responseID, ItemID: itemID, ContentType: "output_text", Text: stringValue(payload["delta"])})
	case strings.Contains(typ, "reasoning") && strings.Contains(typ, "delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventThinkingDelta, ResponseID: responseID, ItemID: itemID, ContentType: "reasoning", Text: stringValue(payload["delta"])})
	case strings.Contains(typ, "function_call_arguments.delta"):
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventToolCallDelta, ResponseID: responseID, ItemID: itemID, ContentType: "function_call", ToolArguments: stringValue(payload["delta"])})
	case typ == "response.completed":
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventResponseComplete, ResponseID: responseID})
	}
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		if response, responseOK := payload["response"].(map[string]any); responseOK {
			usage, ok = response["usage"].(map[string]any)
		}
	}
	if ok {
		event, _, err := openAIUsageFromMap(usage)
		if err != nil {
			return fmt.Errorf("decode OpenAI usage: %w", err)
		}
		emit(kernel.ResponseEvent{At: now, Kind: kernel.EventUsage, Usage: &event})
	}
	return nil
}

func emitOpenAIResponsesJSON(payload map[string]any, emit func(kernel.ResponseEvent), at time.Time, responseID string) error {
	if status := stringValue(payload["status"]); status != "completed" {
		return fmt.Errorf("OpenAI Responses JSON has non-completed status %q", status)
	}
	output, _ := payload["output"].([]any)
	toolIndex := 0
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return fmt.Errorf("OpenAI Responses output contains a non-object item")
		}
		itemType := stringValue(item["type"])
		itemID := stringValue(item["id"])
		switch itemType {
		case "message":
			content, _ := item["content"].([]any)
			for _, rawPart := range content {
				part, ok := rawPart.(map[string]any)
				if !ok {
					return fmt.Errorf("OpenAI Responses message contains a non-object content part")
				}
				switch stringValue(part["type"]) {
				case "output_text":
					if text := stringValue(part["text"]); text != "" {
						emit(kernel.ResponseEvent{At: at, Kind: kernel.EventTextDelta, ResponseID: responseID, ItemID: itemID, ContentType: "output_text", Text: text})
					}
				case "refusal":
					return fmt.Errorf("OpenAI Responses refusal output has no declared canonical mapping")
				default:
					return fmt.Errorf("unsupported OpenAI Responses message content type %q", stringValue(part["type"]))
				}
			}
		case "function_call":
			callID := stringValue(item["call_id"])
			if callID == "" {
				callID = itemID
			}
			emit(kernel.ResponseEvent{At: at, Kind: kernel.EventToolCallDelta, ResponseID: responseID, ItemID: itemID, ContentType: "function_call", Index: toolIndex, ToolCallID: callID, ToolName: stringValue(item["name"]), ToolArguments: stringValue(item["arguments"])})
			toolIndex++
		case "reasoning":
			summary, _ := item["summary"].([]any)
			for _, rawPart := range summary {
				part, ok := rawPart.(map[string]any)
				if !ok || stringValue(part["type"]) != "summary_text" {
					return fmt.Errorf("unsupported OpenAI Responses reasoning summary part")
				}
				if text := stringValue(part["text"]); text != "" {
					emit(kernel.ResponseEvent{At: at, Kind: kernel.EventThinkingDelta, ResponseID: responseID, ItemID: itemID, ContentType: "reasoning", Text: text})
				}
			}
		default:
			return fmt.Errorf("unsupported OpenAI Responses output item type %q", itemType)
		}
	}
	if usage, ok := payload["usage"].(map[string]any); ok {
		event, _, err := openAIUsageFromMap(usage)
		if err != nil {
			return fmt.Errorf("decode OpenAI Responses usage: %w", err)
		}
		emit(kernel.ResponseEvent{At: at, Kind: kernel.EventUsage, ResponseID: responseID, Usage: &event})
	}
	emit(kernel.ResponseEvent{At: at, Kind: kernel.EventResponseComplete, ResponseID: responseID})
	return nil
}

func decodeOpenAIResponse(ctx context.Context, response kernel.UpstreamResponse, wireFormat normalize.Format, emit func(kernel.ResponseEvent) error, hooks kernel.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("provider returned an empty response body")
	}
	contentType := strings.ToLower(response.Headers.Get("content-type"))
	if strings.Contains(contentType, "text/event-stream") {
		usageState := openAIUsageState{}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		scanner.Split(splitSSELines)
		started := false
		completed := false
		usage := kernel.UsageEvent{}
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			rawLine := append([]byte(nil), scanner.Bytes()...)
			line := strings.TrimRight(string(rawLine), "\r\n")
			if !strings.HasPrefix(line, "data:") {
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: rawLine}); err != nil {
					return err
				}
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(line, "\n"), "data:"))
			if data == "" {
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: rawLine}); err != nil {
					return err
				}
				continue
			}
			if data == "[DONE]" {
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: rawLine}); err != nil {
					return err
				}
				completed = true
				if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseComplete}); err != nil {
					return err
				}
				continue
			}
			if !started && hooks.OnFirstByte != nil {
				hooks.OnFirstByte(time.Now())
				started = true
			}
			var payload map[string]any
			decoder := json.NewDecoder(bytes.NewReader([]byte(data)))
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				return fmt.Errorf("decode upstream SSE JSON: %w", err)
			}
			if errPayload, candidate := openAIErrorPayload(payload, []byte(data), true); candidate {
				wire, ok, usageErr := openAIUsageWireFromPayload(payload)
				if usageErr != nil {
					return fmt.Errorf("decode OpenAI provider error usage: %w", usageErr)
				}
				if ok {
					usageState = usageState.apply(&wire)
					if hooks.OnProviderUsage != nil {
						hooks.OnProviderUsage(usageState.snapshot())
					}
				}
				evidence, err := kernel.NewProviderResponseError(errPayload)
				if err != nil {
					return err
				}
				return evidence
			}
			if stringValue(payload["type"]) == "response.incomplete" {
				wire, exists, usageErr := openAIUsageWireFromPayload(payload)
				if usageErr != nil {
					return fmt.Errorf("decode OpenAI incomplete response usage: %w", usageErr)
				}
				if exists {
					usageState = usageState.apply(&wire)
					if hooks.OnProviderUsage != nil {
						hooks.OnProviderUsage(usageState.snapshot())
					}
				}
				return fmt.Errorf("OpenAI Responses SSE response was incomplete")
			}
			if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: rawLine}); err != nil {
				return err
			}
			var events []kernel.ResponseEvent
			if err := emitOpenAIEvent(payload, func(event kernel.ResponseEvent) { events = append(events, event) }); err != nil {
				return fmt.Errorf("decode OpenAI response event: %w", err)
			}
			for _, event := range events {
				if event.Kind == kernel.EventResponseComplete {
					completed = true
				}
				if event.Usage != nil {
					wire, found, usageErr := openAIUsageWireFromPayload(payload)
					if usageErr != nil {
						return fmt.Errorf("decode OpenAI usage snapshot: %w", usageErr)
					}
					if found {
						usageState = usageState.apply(&wire)
						canonical := usageState.snapshot()
						event.Usage = &canonical
						usage = canonical.Clone()
					}
				}
				if err := emit(event); err != nil {
					return err
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if !completed {
			return io.ErrUnexpectedEOF
		}
		if hooks.OnComplete != nil {
			usage.Status = "ok"
			hooks.OnComplete(usage)
		}
		return nil
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode upstream JSON: %w", err)
	}
	if errPayload, candidate := openAIErrorPayload(payload, data, false); candidate {
		wire, ok, usageErr := openAIUsageWireFromPayload(payload)
		if usageErr != nil {
			return fmt.Errorf("decode OpenAI provider error usage: %w", usageErr)
		}
		if ok && hooks.OnProviderUsage != nil {
			hooks.OnProviderUsage((openAIUsageState{}).apply(&wire).snapshot())
		}
		evidence, err := kernel.NewProviderResponseError(errPayload)
		if err != nil {
			return err
		}
		return evidence
	}
	if stringValue(payload["status"]) == "incomplete" {
		wire, exists, usageErr := openAIUsageWireFromPayload(payload)
		if usageErr != nil {
			return fmt.Errorf("decode OpenAI incomplete response usage: %w", usageErr)
		}
		if exists && hooks.OnProviderUsage != nil {
			hooks.OnProviderUsage((openAIUsageState{}).apply(&wire).snapshot())
		}
		return fmt.Errorf("OpenAI Responses JSON has non-completed status %q", "incomplete")
	}
	var events []kernel.ResponseEvent
	if err := emitOpenAIEvent(payload, func(event kernel.ResponseEvent) { events = append(events, event) }); err != nil {
		return fmt.Errorf("decode OpenAI response: %w", err)
	}
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if err := emit(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventRawFrame, WireFormat: wireFormat, Raw: append([]byte(nil), data...)}); err != nil {
		return err
	}
	var usage kernel.UsageEvent
	usageState := openAIUsageState{}
	completed := false
	for _, event := range events {
		if event.Kind == kernel.EventResponseComplete {
			completed = true
		}
		if event.Usage != nil {
			wire, found, usageErr := openAIUsageWireFromPayload(payload)
			if usageErr != nil {
				return fmt.Errorf("decode OpenAI usage snapshot: %w", usageErr)
			}
			if found {
				usageState = usageState.apply(&wire)
				canonical := usageState.snapshot()
				event.Usage = &canonical
				usage = canonical.Clone()
			}
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	if !completed {
		return io.ErrUnexpectedEOF
	}
	if hooks.OnComplete != nil {
		usage.Status = "ok"
		hooks.OnComplete(usage)
	}
	return nil
}

// openAIErrorPayload returns a canonical provider error envelope only for explicit error/status signals.
func openAIErrorPayload(payload map[string]any, data []byte, sse bool) ([]byte, bool) {
	typ := stringValue(payload["type"])
	status := stringValue(payload["status"])
	rootError := payload["error"] != nil
	response, _ := payload["response"].(map[string]any)
	nestedError := response["error"] != nil
	candidate := rootError || nestedError || typ == "error" || typ == "response.failed" || (!sse && status == "failed")
	if !candidate {
		return nil, false
	}
	var envelope struct {
		Error    *json.RawMessage `json:"error"`
		Response struct {
			Error *json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if len(data) > kernel.MaxProviderResponseErrorBytes || json.Unmarshal(data, &envelope) != nil {
		return []byte(`{"error":{}}`), true
	}
	detail := json.RawMessage(`{}`)
	if envelope.Response.Error != nil {
		detail = *envelope.Response.Error
	} else if envelope.Error != nil {
		detail = *envelope.Error
	} else if typ == "error" {
		detail = data
	}
	canonical, err := json.Marshal(struct {
		Error json.RawMessage `json:"error"`
	}{Error: detail})
	if err != nil {
		return []byte(`{"error":{}}`), true
	}
	return canonical, true
}

func openAIUsageWireFromPayload(payload map[string]any) (openAIUsageWire, bool, error) {
	if wire, found, err := openAIUsageWireFromMap(payload["usage"]); found || err != nil {
		return wire, found, err
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if wire, found, err := openAIUsageWireFromMap(response["usage"]); found || err != nil {
			return wire, found, err
		}
		if providerError, ok := response["error"].(map[string]any); ok {
			if wire, found, err := openAIUsageWireFromMap(providerError["usage"]); found || err != nil {
				return wire, found, err
			}
		}
	}
	if providerError, ok := payload["error"].(map[string]any); ok {
		if wire, found, err := openAIUsageWireFromMap(providerError["usage"]); found || err != nil {
			return wire, found, err
		}
	}
	return openAIUsageWire{}, false, nil
}

func openAIUsageWireFromMap(raw any) (openAIUsageWire, bool, error) {
	fields, ok := raw.(map[string]any)
	if !ok {
		return openAIUsageWire{}, false, nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return openAIUsageWire{}, false, err
	}
	wire, err := decodeOpenAIUsageWire(encoded)
	if err != nil {
		return openAIUsageWire{}, false, err
	}
	return wire, true, nil
}

func openAIUsageFromMap(raw any) (kernel.UsageEvent, bool, error) {
	fields, ok := raw.(map[string]any)
	if !ok {
		return kernel.UsageEvent{}, false, nil
	}
	event := kernel.UsageEvent{}
	invalidCounter := func() {
		event.AccountingIssues = append(event.AccountingIssues, kernel.UsageIssue{Code: "invalid_usage_counter"})
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		invalidCounter()
		return event, true, nil
	}
	event, err = decodeOpenAIUsage(encoded)
	if err != nil {
		invalidCounter()
		return event, true, nil
	}
	return event, event.HasReportedUsage() || len(event.AccountingIssues) > 0, nil
}

func splitSSELines(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func integerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		return parsed, err == nil
	case float64:
		if number < 0 || number > float64(int64(^uint64(0)>>1)) || number != float64(int64(number)) {
			return 0, false
		}
		return int64(number), true
	case int:
		return int64(number), true
	default:
		return 0, false
	}
}

func stringValue(value any) string { result, _ := value.(string); return result }
