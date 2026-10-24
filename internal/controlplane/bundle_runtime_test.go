package controlplane

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestBundleRuntimePreviewDoesNotMutateOtherMemoryStores(t *testing.T) {
	live, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err := live.DB.Exec(`INSERT INTO provider_nodes(id,name,base_url,protocol,prefix) VALUES('preview-isolation','Live fixture','http://invalid.example','openai_chat','live')`); err != nil {
		t.Fatal(err)
	}
	strategies, err := kernel.NewBuiltinStrategyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := strategies.Extensions().LockDependencies(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundleRuntime(ConfigBundle{Version: 9, Dependencies: lock}, strategies, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := live.DB.QueryRow(`SELECT COUNT(*) FROM provider_nodes WHERE id='preview-isolation'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("candidate preview mutated another live in-memory database")
	}
}
