package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
)

// ProviderDefinitionPortable reports whether a definition can be embedded in
// the secret-free configuration bundle. Opaque auth defaults, arbitrary
// defaults, endpoint query values and request-codec options remain external
// module data because their sensitivity cannot be proven by the shared schema.
func ProviderDefinitionPortable(definition ProviderDefinition) (bool, string) {
	if len(definition.AuthOptions.Options) > 0 {
		return false, "authOptions.options is opaque and must remain in the installed manifest"
	}
	if len(definition.Defaults) > 0 {
		return false, "provider defaults are opaque and must remain in the installed manifest"
	}
	if definition.AuthOptions.OAuth != nil {
		for _, item := range []struct{ name, raw string }{
			{name: "authUrl", raw: definition.AuthOptions.OAuth.AuthURL}, {name: "tokenUrl", raw: definition.AuthOptions.OAuth.TokenURL},
			{name: "deviceAuthUrl", raw: definition.AuthOptions.OAuth.DeviceAuthURL}, {name: "redirectUrl", raw: definition.AuthOptions.OAuth.RedirectURL},
		} {
			if item.raw == "" {
				continue
			}
			parsed, err := url.Parse(item.raw)
			if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return false, fmt.Sprintf("OAuth %s may contain non-portable credential material", item.name)
			}
		}
	}
	for operation, binding := range definition.Operations {
		if len(binding.RequestCodecOptions) > 0 {
			return false, fmt.Sprintf("operation %q requestCodecOptions remain in the installed manifest", operation)
		}
		if len(binding.EndpointOptions.Query) > 0 {
			return false, fmt.Sprintf("operation %q endpoint query options remain in the installed manifest", operation)
		}
	}
	return true, ""
}

func (r *RuntimeRegistry) LoadDefinitionJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var definitions []ProviderDefinition
		if err := decodeStrictJSON(data, &definitions); err != nil {
			return fmt.Errorf("decode provider definitions: %w", err)
		}
		for _, definition := range definitions {
			if err := r.Primitives.RegisterDefinition(definition); err != nil {
				return err
			}
		}
		return nil
	}
	definition, err := DecodeProviderDefinitionJSON(data)
	if err != nil {
		return err
	}
	return r.Primitives.RegisterDefinition(definition)
}

func (r *RuntimeRegistry) RegisterDefinitionIfAbsent(definition ProviderDefinition) error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("provider runtime registry is not initialized")
	}
	return r.Primitives.RegisterDefinitionIfAbsent(definition)
}

func (r *RuntimeRegistry) HasDefinition(definition ProviderDefinition) bool {
	if r == nil || r.Primitives == nil {
		return false
	}
	existing, ok := r.Primitives.Definition(definition.ID)
	return ok && SameProviderDefinition(existing, definition)
}

func SameProviderDefinition(left, right ProviderDefinition) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (r *RuntimeRegistry) LoadDefinitionFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := r.LoadDefinitionJSON(data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (r *RuntimeRegistry) LoadDefinitionDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := r.LoadDefinitionFile(path); err != nil {
			return err
		}
	}
	return nil
}

func EncodeManifest(definition ProviderDefinition) ([]byte, error) {
	return json.MarshalIndent(definition, "", "  ")
}
