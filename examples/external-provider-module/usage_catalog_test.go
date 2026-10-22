package externalprovider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

var catalogUsageRef = provider.Ref{Kind: "usage-value", ID: "example.catalog-search-units", ContractVersion: 1}

type usageCatalogModule struct{}

func (usageCatalogModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.usage-catalog", Version: "1", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (usageCatalogModule) Register(runtime *provider.RuntimeRegistry) error {
	schema := provider.Ref{Kind: provider.SchemaKind, ID: "example.catalog-search-units.value", ContractVersion: 1}
	if err := runtime.RegisterSchemaDocument(schema, json.RawMessage(`{"type":"object","properties":{"units":{"type":"integer","minimum":0}},"required":["units"],"additionalProperties":false}`)); err != nil {
		return err
	}
	return runtime.RegisterExtension(provider.ExtensionDescriptor{
		Ref: catalogUsageRef, ImplementationVersion: "1", DisplayName: "Search units", Description: "Provider-reported search units, not an automatic billing rule.",
		InputSchemaRef: &schema,
		UsagePolicy:    &provider.UsagePolicy{Unit: "search", MaxValueBytes: 128},
	}, nil)
}

func TestExternalUsageDescriptorCatalogUsesPublicModuleBoundary(t *testing.T) {
	runtime, err := provider.NewRuntimeRegistryWithModules(usageCatalogModule{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateUsageValue(provider.UsageValue{TypeRef: catalogUsageRef, Value: json.RawMessage(`{"units":0}`)}); err != nil {
		t.Fatalf("reported zero rejected: %v", err)
	}
	if err := snapshot.ValidateUsageValue(provider.UsageValue{TypeRef: catalogUsageRef, Value: json.RawMessage(`{"units":-1}`)}); err == nil {
		t.Fatal("negative provider units accepted")
	}
}

func TestInstalledCLIUsageDescriptorCatalog(t *testing.T) {
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		t.Skip("set GOBROOM_TEST_CLI to exercise installed controls")
	}
	root := t.TempDir()
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock")}
	service := daemon.NewWithProviderModules(config, usageCatalogModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, cli, "--ipc", config.IPCPath, "extensions", "catalog").CombinedOutput()
	if err != nil {
		t.Fatalf("installed catalog: %v: %s", err, output)
	}
	var catalog struct {
		Descriptors []provider.ExtensionDescriptor `json:"descriptors"`
		Schemas     []struct {
			Ref provider.Ref `json:"ref"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(output, &catalog); err != nil {
		t.Fatal(err)
	}
	found, schemaFound := false, false
	for _, descriptor := range catalog.Descriptors {
		if descriptor.Ref == catalogUsageRef {
			found = descriptor.UsagePolicy != nil && descriptor.UsagePolicy.Unit == "search" && descriptor.InputSchemaRef != nil
		}
	}
	for _, schema := range catalog.Schemas {
		if schema.Ref.ID == "example.catalog-search-units.value" && schema.Ref.ContractVersion == 1 {
			schemaFound = true
		}
	}
	if !found || !schemaFound {
		t.Fatalf("catalog missing typed metadata or exposed runtime data: descriptor=%v schema=%v", found, schemaFound)
	}
}
