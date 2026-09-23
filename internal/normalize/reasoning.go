package normalize

// ReasoningPolicy represents the layered defaults around one request. Empty
// or inherit intent yields to the next layer; explicit disabled/auto/level/
// budget always wins over defaults.
type ReasoningPolicy struct {
	Explicit ThinkingIntent
	Preset   ThinkingIntent
	Combo    ThinkingIntent
	Physical ThinkingIntent
	Provider ThinkingIntent
}

func ResolveReasoning(policy ReasoningPolicy) ThinkingIntent {
	for _, candidate := range []ThinkingIntent{policy.Explicit, policy.Preset, policy.Combo, policy.Physical, policy.Provider} {
		if candidate.Mode != "" && candidate.Mode != "inherit" {
			return candidate
		}
	}
	return ThinkingIntent{Mode: "inherit", Source: "absent"}
}

func AnthropicReasoning(intent ThinkingIntent) map[string]any {
	switch intent.Mode {
	case "disabled":
		return map[string]any{"thinking": map[string]any{"type": "disabled"}}
	case "budget":
		if intent.BudgetTokens > 0 {
			return map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": intent.BudgetTokens}}
		}
	case "auto":
		return map[string]any{"thinking": map[string]any{"type": "adaptive"}}
	case "level":
		result := map[string]any{"thinking": map[string]any{"type": "adaptive"}}
		if intent.Effort != "" {
			result["output_config"] = map[string]any{"effort": intent.Effort}
		}
		return result
	}
	return nil
}

func OpenAIResponsesReasoning(intent ThinkingIntent) map[string]any {
	switch intent.Mode {
	case "disabled":
		return map[string]any{"reasoning": map[string]any{"effort": "none"}}
	case "auto":
		return map[string]any{"reasoning": map[string]any{"effort": "auto"}}
	case "level":
		if intent.Effort != "" {
			return map[string]any{"reasoning": map[string]any{"effort": intent.Effort}}
		}
	case "budget":
		if intent.BudgetTokens > 0 {
			return map[string]any{"reasoning": map[string]any{"max_output_tokens": intent.BudgetTokens}}
		}
	}
	return nil
}

func GeminiReasoning(intent ThinkingIntent) map[string]any {
	if intent.Mode == "inherit" || intent.Mode == "" {
		return nil
	}
	thinking := map[string]any{}
	switch intent.Mode {
	case "disabled":
		thinking["thinkingBudget"] = 0
	case "budget":
		thinking["thinkingBudget"] = intent.BudgetTokens
	case "level", "auto":
		thinking["thinkingLevel"] = intent.Effort
	}
	return map[string]any{"generationConfig": map[string]any{"thinkingConfig": thinking}}
}
