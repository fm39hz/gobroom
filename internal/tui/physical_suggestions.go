package tui

import (
	"strings"
	"unicode"
)

// suggestPhysicalName proposes a user-editable identity only when every
// selected upstream route reduces to the same conservative model token. It
// never merges routes by itself; the user must review and save the grouping.
func suggestPhysicalName(routes []discoveredRoute) string {
	if len(routes) == 0 {
		return ""
	}
	name := canonicalPhysicalCandidate(routes[0].ExternalID)
	if name == "" {
		return ""
	}
	for _, route := range routes[1:] {
		if canonicalPhysicalCandidate(route.ExternalID) != name {
			return ""
		}
	}
	return name
}

func suggestedPhysicalSource(route discoveredRoute, canonicalName string) routeReference {
	if strings.EqualFold(strings.TrimSpace(route.ExternalID), canonicalName) {
		return routeReference{RouteID: route.ID, Fidelity: "exact"}
	}
	return routeReference{RouteID: route.ID, Fidelity: "alias", Evidence: []map[string]any{{
		"source": "user_assertion", "confidence": 0.5,
		"note":  "selected after reviewing the canonical-name suggestion " + canonicalName,
	}}}
}

func canonicalPhysicalCandidate(externalID string) string {
	value := strings.TrimSpace(externalID)
	if value == "" {
		return ""
	}
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		value = value[slash+1:]
	}
	if strings.HasSuffix(strings.ToLower(value), ":free") {
		value = value[:len(value)-len(":free")]
	}
	if colon := strings.IndexByte(value, ':'); colon > 0 {
		prefix, candidate := value[:colon], value[colon+1:]
		prefixIsNamespaced := strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(prefix))
		for _, char := range prefix {
			prefixIsNamespaced = prefixIsNamespaced || unicode.IsUpper(char)
		}
		if prefixIsNamespaced {
			value = candidate
		}
	}
	value = strings.ToLower(value)
	value = strings.TrimSuffix(value, "-free")
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '.' {
			continue
		}
		return ""
	}
	prefixEnd := 0
	for prefixEnd < len(value) && value[prefixEnd] >= 'a' && value[prefixEnd] <= 'z' {
		prefixEnd++
	}
	if prefixEnd > 0 && prefixEnd < len(value) && value[prefixEnd] >= '0' && value[prefixEnd] <= '9' {
		value = value[:prefixEnd] + "-" + value[prefixEnd:]
	}
	return value
}
