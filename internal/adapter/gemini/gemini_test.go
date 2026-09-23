package gemini

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestPrepareMapsReasoningToGeminiThinkingConfig(t *testing.T) {
	request := normalize.Request{SourceFormat: normalize.FormatGemini, Raw: map[string]any{}, Thinking: normalize.ThinkingIntent{Mode: "budget", BudgetTokens: 512}}
	prepared, err := (Gemini{}).Prepare(context.Background(), request, kernel.Route{ID: "r", BaseURL: "https://gemini.test", ExternalModel: "gemini-2"}, kernel.Credential{Secret: "key"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(prepared.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	config, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("body=%#v", body)
	}
	thinking, ok := config["thinkingConfig"].(map[string]any)
	if !ok || thinking["thinkingBudget"] != float64(512) {
		t.Fatalf("thinking=%#v", config)
	}
}
