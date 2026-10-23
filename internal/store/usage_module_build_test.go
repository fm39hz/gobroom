package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestUsageHistorySameDataContractSurvivesModuleRebuild(t *testing.T) {
	path := t.TempDir() + "/module.db"
	for _, build := range []string{"build-a", "build-b"} {
		catalog := extensions.NewCatalog()
		err := catalog.RegisterModule(extensions.ModuleRef{ID: "fixture.data-owner", Version: build, HostContractVersion: 12}, func() error {
			document, err := extensions.BindSchemaDocument(usageHistorySchemaRef, json.RawMessage(`{"type":"number"}`))
			if err != nil {
				return err
			}
			if err := catalog.RegisterSchema(usageHistorySchemaRef, document); err != nil {
				return err
			}
			return catalog.RegisterDescriptor(extensions.Descriptor{Ref: usageHistoryRef, ImplementationVersion: "1", DisplayName: "Units", Description: "Stable data contract across code rebuilds.", InputSchemaRef: &usageHistorySchemaRef, UsagePolicy: &extensions.UsagePolicy{MaxValueBytes: 1024}})
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := catalog.Freeze()
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetUsageCatalog(snapshot); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveUsageEvent(context.Background(), usageHistoryEvent(`9007199254740993.125`)); err != nil {
			_ = s.Close()
			t.Fatalf("module rebuild changed no data semantics but history blocked a valid write: %s: %v", build, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
