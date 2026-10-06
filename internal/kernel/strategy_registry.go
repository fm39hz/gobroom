package kernel

import (
	"fmt"
	"math"
)

type StrategyOptionType string

const StrategyOptionInteger StrategyOptionType = "integer"

type StrategyOptionDefinition struct {
	Key         string             `json:"key"`
	Label       string             `json:"label"`
	Description string             `json:"description,omitempty"`
	Type        StrategyOptionType `json:"type"`
	Default     any                `json:"default,omitempty"`
	Minimum     int                `json:"minimum,omitempty"`
	Maximum     int                `json:"maximum,omitempty"`
}

// StrategyDefinition is the single built-in strategy contract used by the
// scheduler, configuration validation, IPC catalog and user interfaces.
// Runtime is intentionally omitted from JSON; clients select by stable ID.
type StrategyDefinition struct {
	ID          string                     `json:"id"`
	Label       string                     `json:"label"`
	Description string                     `json:"description"`
	Options     []StrategyOptionDefinition `json:"options,omitempty"`
	Runtime     Strategy                   `json:"-"`
	Primitive   StrategyPrimitive          `json:"-"`
}

var builtinStrategyDefinitions = []StrategyDefinition{
	{
		ID: "ordered-fallback", Label: "Ordered fallback",
		Description: "Try members in their configured order; continue only for retryable failures.",
		Runtime:     StrategyFallback, Primitive: OrderedFallback{},
	},
	{
		ID: "rotating-fallback", Label: "Rotating fallback",
		Description: "Rotate the first member on each request, then try the remaining members in order.",
		Runtime:     StrategyRotatingFallback, Primitive: RotatingFallback{},
	},
	{
		ID: "round-robin", Label: "Round robin",
		Description: "Choose one member per request; on failure, control returns to the parent Combo policy rather than trying sibling members.",
		Runtime:     StrategyRoundRobin, Primitive: RoundRobin{},
	},
	{
		ID: "round-robin-fallback", Label: "Sticky round-robin fallback",
		Description: "Keep the current starting member for a bounded number of requests, then rotate and fall back in order.",
		Runtime:     StrategyRoundRobinFallback, Primitive: StickyRoundRobinFallback{},
		Options: []StrategyOptionDefinition{{
			Key: "stickyLimit", Label: "Requests per starting member",
			Description: "How many requests use one starting member before rotating.",
			Type:        StrategyOptionInteger, Default: 1, Minimum: 1, Maximum: 1000,
		}},
	},
	{
		ID: "weighted-fallback", Label: "Weighted fallback",
		Description: "Choose a weighted starting member, then try remaining members as fallbacks; weights are edited on member edges.",
		Runtime:     StrategyWeighted, Primitive: WeightedFallback{},
	},
}

func StrategyDefinitions() []StrategyDefinition {
	result := make([]StrategyDefinition, 0, len(builtinStrategyDefinitions))
	for _, definition := range builtinStrategyDefinitions {
		copy := definition
		copy.Options = append([]StrategyOptionDefinition(nil), definition.Options...)
		result = append(result, copy)
	}
	return result
}

func StrategyDefinitionByID(id string) (StrategyDefinition, bool) {
	for _, definition := range builtinStrategyDefinitions {
		if definition.ID == id {
			copy := definition
			copy.Options = append([]StrategyOptionDefinition(nil), definition.Options...)
			return copy, true
		}
	}
	return StrategyDefinition{}, false
}

func ValidateStrategyConfig(id string, config map[string]any) error {
	definition, ok := StrategyDefinitionByID(id)
	if !ok {
		return fmt.Errorf("unknown strategy primitive %q", id)
	}
	options := make(map[string]StrategyOptionDefinition, len(definition.Options))
	for _, option := range definition.Options {
		options[option.Key] = option
	}
	for key, value := range config {
		option, ok := options[key]
		if !ok {
			return fmt.Errorf("strategy %q has no option %q", id, key)
		}
		if option.Type != StrategyOptionInteger {
			return fmt.Errorf("strategy %q option %q has unsupported schema type %q", id, key, option.Type)
		}
		number, ok := integerOptionValue(value)
		if !ok {
			return fmt.Errorf("strategy %q option %q must be an integer", id, key)
		}
		if number < option.Minimum || (option.Maximum > 0 && number > option.Maximum) {
			return fmt.Errorf("strategy %q option %q must be between %d and %d", id, key, option.Minimum, option.Maximum)
		}
	}
	return nil
}

func StrategyIntegerOption(id string, config map[string]any, key string) (int, error) {
	if err := ValidateStrategyConfig(id, config); err != nil {
		return 0, err
	}
	definition, _ := StrategyDefinitionByID(id)
	for _, option := range definition.Options {
		if option.Key != key {
			continue
		}
		if value, ok := config[key]; ok {
			number, _ := integerOptionValue(value)
			return number, nil
		}
		if number, ok := integerOptionValue(option.Default); ok {
			return number, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("strategy %q has no integer option %q", id, key)
}

func DefaultStrategyConfig(id string) (map[string]any, error) {
	definition, ok := StrategyDefinitionByID(id)
	if !ok {
		return nil, fmt.Errorf("unknown strategy primitive %q", id)
	}
	config := make(map[string]any, len(definition.Options))
	for _, option := range definition.Options {
		if option.Default != nil {
			config[option.Key] = option.Default
		}
	}
	return config, nil
}

func integerOptionValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int8:
		return int(number), true
	case int16:
		return int(number), true
	case int32:
		return int(number), true
	case int64:
		return int(number), int64(int(number)) == number
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number > math.MaxInt || number < math.MinInt {
			return 0, false
		}
		return int(number), true
	default:
		return 0, false
	}
}
