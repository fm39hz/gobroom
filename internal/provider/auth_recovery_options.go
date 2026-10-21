package provider

import (
	"fmt"
	"strings"
)

func validateHTTPAuthRecoverySignals(signals []HTTPJSONErrorSignal) error {
	return validateHTTPJSONErrorSignals("auth recovery", signals)
}

func validateHTTPContinuityResetSignals(signals []HTTPJSONErrorSignal) error {
	if err := validateHTTPJSONErrorSignals("continuity reset", signals); err != nil {
		return err
	}
	for index, signal := range signals {
		if len(signal.HTTPStatuses) == 0 {
			return fmt.Errorf("continuity reset signal %d requires HTTP statuses", index)
		}
		if len(signal.Codes)+len(signal.Types)+len(signal.Statuses)+len(signal.Fields) == 0 {
			return fmt.Errorf("continuity reset signal %d requires structured body evidence", index)
		}
		for _, status := range signal.HTTPStatuses {
			if status < 400 || status > 499 || status == 401 || status == 403 || status == 408 || status == 429 {
				return fmt.Errorf("continuity reset signal %d has forbidden HTTP status", index)
			}
		}
	}
	return nil
}

func validateHTTPResponseContinuityResetSignals(signals []HTTPJSONErrorSignal) error {
	if err := validateHTTPJSONErrorSignals("response continuity reset", signals); err != nil {
		return err
	}
	for index, signal := range signals {
		if len(signal.HTTPStatuses) != 0 {
			return fmt.Errorf("response continuity reset signal %d must not declare HTTP statuses", index)
		}
		if len(signal.Codes)+len(signal.Types)+len(signal.Statuses)+len(signal.Fields) == 0 {
			return fmt.Errorf("response continuity reset signal %d requires structured body evidence", index)
		}
	}
	return nil
}

func validateHTTPJSONErrorSignals(label string, signals []HTTPJSONErrorSignal) error {
	if signals == nil {
		return nil
	}
	if len(signals) == 0 || len(signals) > 64 {
		return fmt.Errorf("%s requires 1..64 declared signals", label)
	}
	for index, signal := range signals {
		if (signal.HTTPStatuses != nil && len(signal.HTTPStatuses) == 0) || (signal.Fields != nil && len(signal.Fields) == 0) {
			return fmt.Errorf("%s signal %d has an empty selector", label, index)
		}
		if len(signal.HTTPStatuses)+len(signal.Codes)+len(signal.Types)+len(signal.Statuses)+len(signal.Fields) == 0 {
			return fmt.Errorf("%s signal %d has no matching evidence", label, index)
		}
		if len(signal.HTTPStatuses) > 32 || len(signal.Fields) > 64 {
			return fmt.Errorf("%s signal %d exceeds matcher bounds", label, index)
		}
		seen := make(map[int]bool, len(signal.HTTPStatuses))
		for _, status := range signal.HTTPStatuses {
			if status < 400 || status > 599 || seen[status] {
				return fmt.Errorf("%s signal %d has invalid or repeated HTTP status", label, index)
			}
			seen[status] = true
		}
		for _, values := range [][]string{signal.Codes, signal.Types, signal.Statuses} {
			if err := validateAuthRecoveryValues(values); err != nil {
				return fmt.Errorf("%s signal %d: %w", label, index, err)
			}
		}
		for _, field := range signal.Fields {
			if !validJSONPointer(field.Path) || len(field.Equals)+len(field.Contains) == 0 {
				return fmt.Errorf("%s signal %d has invalid field matcher", label, index)
			}
			for _, values := range [][]string{field.Equals, field.Contains} {
				if err := validateAuthRecoveryValues(values); err != nil {
					return fmt.Errorf("%s signal %d field: %w", label, index, err)
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
