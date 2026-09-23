package provider

import (
	"fmt"
	"strings"
)

// Profile is a built-in provider preset. Presets define defaults and protocol
// behavior; credentials, models and combos remain user-owned SQLite data.
type Profile struct {
	ID, DisplayName, Protocol, ModelsPath string
	AuthMode                              string
	Aliases                               []string
}

var Profiles = map[string]Profile{
	"openai":      {ID: "openai", DisplayName: "OpenAI", Protocol: "openai_chat", ModelsPath: "/v1/models", AuthMode: "api_key"},
	"anthropic":   {ID: "anthropic", DisplayName: "Anthropic", Protocol: "anthropic", ModelsPath: "", AuthMode: "api_key"},
	"openrouter":  {ID: "openrouter", DisplayName: "OpenRouter", Protocol: "openai_chat", ModelsPath: "/models", AuthMode: "api_key"},
	"openai_chat": {ID: "openai_chat", DisplayName: "OpenAI-compatible Chat", Protocol: "openai_chat", ModelsPath: "/models", AuthMode: "api_key"},
	"responses":   {ID: "responses", DisplayName: "OpenAI-compatible Responses", Protocol: "openai_responses", ModelsPath: "/models", AuthMode: "api_key"},
	"opencode-go": {ID: "opencode-go", DisplayName: "OpenCode Go", Protocol: "openai_chat", ModelsPath: "/models", AuthMode: "api_key", Aliases: []string{"ocg"}},
	"opencode":    {ID: "opencode", DisplayName: "OpenCode Free", Protocol: "openai_chat", ModelsPath: "/models", AuthMode: "none", Aliases: []string{"oc"}},
	"gemini":      {ID: "gemini", DisplayName: "Google Gemini", Protocol: "gemini", ModelsPath: "/v1beta/models", AuthMode: "api_key"},
}

func ProfileFor(id string) (Profile, bool) { p, ok := Profiles[id]; return p, ok }

type PrefixKind string

const (
	PrefixBuiltIn PrefixKind = "builtin"
	PrefixCustom  PrefixKind = "custom"
)

type PrefixEntry struct {
	Prefix    string
	Canonical string
	Kind      PrefixKind
}

type PrefixRegistry struct {
	entries map[string]PrefixEntry
}

func NewPrefixRegistry() *PrefixRegistry {
	r := &PrefixRegistry{entries: map[string]PrefixEntry{}}
	for id, profile := range Profiles {
		r.entries[id] = PrefixEntry{Prefix: id, Canonical: profile.ID, Kind: PrefixBuiltIn}
		for _, alias := range profile.Aliases {
			r.entries[alias] = PrefixEntry{Prefix: alias, Canonical: profile.ID, Kind: PrefixBuiltIn}
		}
	}
	return r
}

func (r *PrefixRegistry) AddBuiltIn(entry PrefixEntry, aliases ...string) error {
	entry.Kind = PrefixBuiltIn
	if err := r.add(entry); err != nil {
		return err
	}
	for _, alias := range aliases {
		copy := entry
		copy.Prefix = alias
		if err := r.add(copy); err != nil {
			return err
		}
	}
	return nil
}

func (r *PrefixRegistry) AddCustom(prefix, canonical string) error {
	prefix = strings.TrimSpace(prefix)
	canonical = strings.TrimSpace(canonical)
	if prefix == "" || canonical == "" {
		return fmt.Errorf("prefix and canonical ID are required")
	}
	if existing, ok := r.entries[prefix]; ok {
		return fmt.Errorf("prefix %q already belongs to %s (%s)", prefix, existing.Canonical, existing.Kind)
	}
	r.entries[prefix] = PrefixEntry{Prefix: prefix, Canonical: canonical, Kind: PrefixCustom}
	return nil
}

// AddNode registers a configured provider node. A node can reuse a built-in
// prefix when its generic definition is compatible with that prefix.
func (r *PrefixRegistry) AddNode(prefix, definitionID string) error {
	prefix = strings.TrimSpace(prefix)
	definitionID = strings.TrimSpace(definitionID)
	if prefix == "" || definitionID == "" {
		return fmt.Errorf("prefix and definition ID are required")
	}
	if existing, ok := r.entries[prefix]; ok {
		if existing.Kind == PrefixBuiltIn && compatibleNodeDefinition(existing.Canonical, definitionID) {
			return nil
		}
		return fmt.Errorf("prefix %q already belongs to %s (%s)", prefix, existing.Canonical, existing.Kind)
	}
	r.entries[prefix] = PrefixEntry{Prefix: prefix, Canonical: definitionID, Kind: PrefixCustom}
	return nil
}

func compatibleNodeDefinition(canonical, definitionID string) bool {
	if canonical == definitionID {
		return true
	}
	return definitionID == "openai-compatible-chat" && canonical != "anthropic" && canonical != "responses"
}

func (r *PrefixRegistry) Resolve(prefix string) (PrefixEntry, bool) {
	entry, ok := r.entries[prefix]
	return entry, ok
}

func (r *PrefixRegistry) Entries() []PrefixEntry {
	result := make([]PrefixEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		result = append(result, entry)
	}
	return result
}

func (r *PrefixRegistry) add(entry PrefixEntry) error {
	if strings.TrimSpace(entry.Prefix) == "" || strings.TrimSpace(entry.Canonical) == "" {
		return fmt.Errorf("prefix and canonical ID are required")
	}
	if existing, ok := r.entries[entry.Prefix]; ok && existing.Canonical != entry.Canonical {
		return fmt.Errorf("prefix %q already belongs to %s", entry.Prefix, existing.Canonical)
	}
	r.entries[entry.Prefix] = entry
	return nil
}
