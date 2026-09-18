package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func (r *RuntimeRegistry) LoadDefinitionJSON(data []byte) error {
	var definitions []ProviderDefinition
	if err := json.Unmarshal(data, &definitions); err == nil {
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
