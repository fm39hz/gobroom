package externalprovider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

type portabilityDependencyLock struct {
	Version    int                       `json:"version"`
	Roots      []provider.Ref            `json:"roots"`
	Extensions []portabilityExtensionPin `json:"extensions"`
	Schemas    []portabilitySchemaPin    `json:"schemas"`
	Modules    []provider.ModuleRef      `json:"modules"`
}

type portabilityExtensionPin struct {
	Ref                   provider.Ref `json:"ref"`
	ImplementationVersion string       `json:"implementationVersion"`
	DescriptorSHA256      string       `json:"descriptorSha256"`
}

type portabilitySchemaPin struct {
	Ref    provider.Ref `json:"ref"`
	SHA256 string       `json:"sha256"`
}

func TestIndependentModuleBundleRejectsChangedPinsWithoutMutation(t *testing.T) {
	source := newFailureClientFixture(t, 202)
	exported := portabilityOK(t, source.ipc, "config.export", nil)

	tests := []struct {
		name            string
		moduleOnly      bool
		mutate          func(*testing.T, *portabilityDependencyLock)
		runtimeMutation func(*testing.T, json.RawMessage) json.RawMessage
	}{
		{name: "unsupported credential", runtimeMutation: func(t *testing.T, raw json.RawMessage) json.RawMessage {
			return mutatePortabilityRuntimeField(t, raw, "connections", "CredentialType", "unsupported-credential")
		}},
		{name: "unsupported protocol", runtimeMutation: func(t *testing.T, raw json.RawMessage) json.RawMessage {
			return mutatePortabilityRuntimeField(t, raw, "providers", "Protocol", "unsupported-protocol")
		}},
		{
			name: "module build version",
			mutate: func(t *testing.T, lock *portabilityDependencyLock) {
				t.Helper()
				index := portabilityModuleIndex(t, lock, "example.jobs.failure-client")
				lock.Modules[index].Version += "-changed"
			},
		},
		{
			name: "module host contract",
			mutate: func(t *testing.T, lock *portabilityDependencyLock) {
				t.Helper()
				index := portabilityModuleIndex(t, lock, "example.jobs.failure-client")
				lock.Modules[index].HostContractVersion++
			},
		},
		{
			name: "reachable response decoder digest",
			mutate: func(t *testing.T, lock *portabilityDependencyLock) {
				t.Helper()
				for i := range lock.Extensions {
					if lock.Extensions[i].Ref == (provider.Ref{Kind: "response_decoder", ID: "example.jobs.failure-client.create", ContractVersion: 2}) {
						lock.Extensions[i].DescriptorSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
						return
					}
				}
				t.Fatal("export omitted the configured job response-decoder pin")
			},
		},
		{
			name: "reachable job input schema digest",
			mutate: func(t *testing.T, lock *portabilityDependencyLock) {
				t.Helper()
				for i := range lock.Schemas {
					if lock.Schemas[i].Ref == (provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.create.input", ContractVersion: 1}) {
						lock.Schemas[i].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
						return
					}
				}
				t.Fatal("export omitted the configured job input-schema pin")
			},
		},
		{
			name:       "linked failure module missing",
			moduleOnly: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bundle := append(json.RawMessage(nil), exported...)
			if tc.mutate != nil {
				bundle = mutatePortabilityBundleLock(t, bundle, tc.mutate)
			}
			if tc.runtimeMutation != nil {
				bundle = tc.runtimeMutation(t, bundle)
			}

			baseline := newPortabilityHost(t)
			before := portabilityParams(t, portabilityOK(t, baseline.config.IPCPath, "config.export", nil))
			if err := baseline.service.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			bundlePath := filepath.Join(t.TempDir(), "bad-bundle.json")
			if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
				t.Fatal(err)
			}

			modules := []provider.RuntimeModule{JobModule{}, newFailureClientModule{}}
			if tc.moduleOnly {
				modules = []provider.RuntimeModule{JobModule{}}
			}
			candidateConfig := baseline.config
			candidateConfig.ConfigBundlePath = bundlePath
			candidate := daemon.NewWithProviderModules(candidateConfig, modules...)
			if err := candidate.Start(t.Context()); err == nil {
				_ = candidate.Stop(t.Context())
				t.Fatal("startup accepted a bundle with unavailable or changed exact pins")
			}

			if baseline.config.ConfigBundlePath != "" {
				t.Fatal("stock host unexpectedly has a startup bundle path")
			}
			baseline.restart(t)
			after := portabilityParams(t, portabilityOK(t, baseline.config.IPCPath, "config.export", nil))
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected startup bundle mutated stock target export: before=%#v after=%#v", before, after)
			}
			if got := source.calls.Load(); got != 0 {
				t.Fatalf("bundle export/import dispatched %d inference requests", got)
			}
		})
	}
}

func mutatePortabilityRuntimeField(t *testing.T, raw json.RawMessage, collection, key, value string) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(fields[collection], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("fixture %s: %v", collection, err)
	}
	if _, exists := rows[0][key]; !exists {
		t.Fatalf("fixture missing %s.%s", collection, key)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	rows[0][key] = encoded
	fields[collection], err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mutatePortabilityBundleLock(t *testing.T, bundle json.RawMessage, mutate func(*testing.T, *portabilityDependencyLock)) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bundle, &fields); err != nil {
		t.Fatal(err)
	}
	var lock portabilityDependencyLock
	if err := json.Unmarshal(fields["dependencies"], &lock); err != nil {
		t.Fatal(err)
	}
	mutate(t, &lock)
	encoded, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	fields["dependencies"] = encoded
	result, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func portabilityModuleIndex(t *testing.T, lock *portabilityDependencyLock, id string) int {
	t.Helper()
	for i, module := range lock.Modules {
		if module.ID == id {
			return i
		}
	}
	t.Fatalf("dependency lock omitted module %q", id)
	return -1
}
