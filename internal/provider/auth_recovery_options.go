package provider

import (
	"fmt"
	"strings"
)

func validateHTTPAuthRecoverySignals(signals []HTTPJSONAuthRecoverySignal) error {
	if signals == nil {
		return nil
	}
	if len(signals) == 0 || len(signals) > 64 {
		return fmt.Errorf("auth recovery requires 1..64 declared signals")
	}
	for index, signal := range signals {
		if (signal.HTTPStatuses != nil && len(signal.HTTPStatuses) == 0) || (signal.Fields != nil && len(signal.Fields) == 0) {
			return fmt.Errorf("auth recovery signal %d has an empty selector", index)
		}
		if len(signal.HTTPStatuses)+len(signal.Codes)+len(signal.Types)+len(signal.Statuses)+len(signal.Fields) == 0 {
			return fmt.Errorf("auth recovery signal %d has no matching evidence", index)
		}
		if len(signal.HTTPStatuses) > 32 || len(signal.Fields) > 64 {
			return fmt.Errorf("auth recovery signal %d exceeds matcher bounds", index)
		}
		seen := make(map[int]bool, len(signal.HTTPStatuses))
		for _, status := range signal.HTTPStatuses {
			if status < 400 || status > 599 || seen[status] {
				return fmt.Errorf("auth recovery signal %d has invalid or repeated HTTP status", index)
			}
			seen[status] = true
		}
		for _, values := range [][]string{signal.Codes, signal.Types, signal.Statuses} {
			if err := validateAuthRecoveryValues(values); err != nil {
				return fmt.Errorf("auth recovery signal %d: %w", index, err)
			}
		}
		for _, field := range signal.Fields {
			if !validJSONPointer(field.Path) || len(field.Equals)+len(field.Contains) == 0 {
				return fmt.Errorf("auth recovery signal %d has invalid field matcher", index)
			}
			for _, values := range [][]string{field.Equals, field.Contains} {
				if err := validateAuthRecoveryValues(values); err != nil {
					return fmt.Errorf("auth recovery signal %d field: %w", index, err)
				}
			}
		}
	}
	return nil
}

func validateAuthRecoveryValues(values []string) error {
	if values != nil && len(values) == 0 {
		return fmt.Errorf("auth recovery selector must not be empty")
	}
	if len(values) > 64 {
		return fmt.Errorf("too many auth recovery alternatives")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("auth recovery alternatives must not be empty")
		}
	}
	return nil
}
