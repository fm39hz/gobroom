package provider

import (
	"fmt"
	"strings"
)

// ModelRef is the wire-level model reference understood by compatible clients.
// Only the first slash is structural; the remainder is an opaque upstream
// model ID and may contain more slashes or colons.
type ModelRef struct {
	Raw         string
	Prefix      string
	Model       string
	HasPrefix   bool
	CanonicalID string
}

// ParseModel preserves the 9router model syntax: prefix/model is split at the
// first slash, while a bare model remains opaque for compatibility inference.
func ParseModel(raw string) (ModelRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ModelRef{}, fmt.Errorf("model is required")
	}
	ref := ModelRef{Raw: raw, Model: raw}
	if index := strings.IndexByte(raw, '/'); index >= 0 {
		if index == 0 || index == len(raw)-1 {
			return ModelRef{}, fmt.Errorf("invalid model reference %q", raw)
		}
		ref.Prefix = raw[:index]
		ref.Model = raw[index+1:]
		ref.HasPrefix = true
	}
	return ref, nil
}

// ResolvePrefix resolves a wire prefix without changing the model ID.
func ResolvePrefix(ref ModelRef, registry *PrefixRegistry) (ModelRef, error) {
	if !ref.HasPrefix || registry == nil {
		return ref, nil
	}
	entry, ok := registry.Resolve(ref.Prefix)
	if !ok {
		return ModelRef{}, fmt.Errorf("unknown provider prefix %q", ref.Prefix)
	}
	ref.CanonicalID = entry.Canonical
	return ref, nil
}

// InferProvider returns the deliberately small compatibility fallback used by
// 9router for bare model names. It is not used by the strict snapshot resolver
// unless a caller explicitly opts into inference.
func InferProvider(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "claude-"):
		return "anthropic"
	case strings.HasPrefix(lower, "gemini-"):
		return "gemini"
	case strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4"):
		return "openai"
	case strings.HasPrefix(lower, "deepseek-"):
		return "openrouter"
	default:
		return "openai"
	}
}

// ResolveAlias resolves the string form used by 9router's modelAliases map.
// The first slash rule is shared with ParseModel, so upstream IDs remain
// opaque after the provider prefix.
func ResolveAlias(alias string, aliases map[string]string) (ModelRef, bool, error) {
	target, ok := aliases[alias]
	if !ok {
		return ModelRef{}, false, nil
	}
	ref, err := ParseModel(target)
	if err != nil {
		return ModelRef{}, true, err
	}
	return ref, true, nil
}
