package openai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestResponsesPrepareTranslatesChatReasoningIntent(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatOpenAIChat, Raw: map[string]any{"messages": []any{}}, Thinking: normalize.ThinkingIntent{Mode: "level", Effort: "high"}}
	prepared, err := (Responses{}).Prepare(context.Background(), request, kernel.Route{ID: "route", BaseURL: "https://provider.test", ExternalModel: "gpt"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != nil {
		t.Fatalf("chat reasoning field leaked: %#v", body)
	}
	if reasoning, ok := body["reasoning"].(map[string]any); !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning=%#v", body["reasoning"])
	}
}

func TestResponsesPrepareUsesSessionStoreContinuityWhenClientOmitsIt(t *testing.T) {
	request := normalize.Request{
		SourceFormat: normalize.FormatOpenAIChat,
		Raw:          map[string]any{"messages": []any{map[string]any{"role": "user", "content": "continue"}}},
		Continuity:   normalize.ContinuityState{PreviousResponse: "resp-from-session"},
	}
	prepared, err := (Responses{}).Prepare(context.Background(), request, kernel.Route{ID: "route", ExternalModel: "gpt"}, kernel.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["previous_response_id"] != "resp-from-session" {
		t.Fatalf("previous_response_id=%#v", body["previous_response_id"])
	}
}
