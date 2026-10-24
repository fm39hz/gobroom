package daemon

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

const maxStartupBundleBytes = 32 << 20

func readStartupBundle(path string) (*controlplane.ConfigBundle, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open startup config bundle failed")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxStartupBundleBytes+1))
	if err != nil {
		return nil, errors.New("read startup config bundle failed")
	}
	if len(raw) > maxStartupBundleBytes {
		return nil, errors.New("startup config bundle exceeds 32 MiB")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("startup config bundle must be an object")
	}
	var bundle controlplane.ConfigBundle
	if err := json.Unmarshal(raw, &bundle, json.RejectUnknownMembers(true)); err != nil {
		// Parser diagnostics may quote private user-controlled input.
		return nil, errors.New("invalid startup config bundle JSON")
	}
	return &bundle, nil
}

func applyStartupBundle(s *store.Store, bundle controlplane.ConfigBundle, strategies *kernel.StrategyCatalog, catalog *extensions.Snapshot, bindings map[string]provider.RuntimeBinding) error {
	if err := controlplane.ValidateBundleRuntime(bundle, strategies, bindings); err != nil {
		return err
	}
	requestTransforms, err := kernel.NewRequestTransformRegistryFromCatalog(catalog)
	if err != nil {
		return err
	}
	responseTransforms, err := kernel.NewResponseTransformRegistryFromCatalog(catalog)
	if err != nil {
		return err
	}
	// This validation uses only the two bound registries, not execution state.
	// Reuse the same validator as live config.apply before mutating SQLite.
	validator := kernel.Kernel{Transforms: requestTransforms, ResponseTransforms: responseTransforms}
	if err := validator.ValidateTransformBindings(bundle.TransformBindings); err != nil {
		return err
	}
	if err := controlplane.ApplyBundle(s, bundle, strategies); err != nil {
		return fmt.Errorf("apply startup config bundle: %w", err)
	}
	return nil
}
