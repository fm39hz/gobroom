package daemon

import (
	"testing"

	"github.com/gorouter/gorouter/internal/api"
	"github.com/gorouter/gorouter/internal/store"
)

func TestIPCControlCRUDUsesDaemonServices(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := &Daemon{store: s, server: api.NewServer(s)}
	response := d.handleIPC(nil, IPCRequest{ID: "1", Method: "providers.create", Params: map[string]any{"name": "G4F", "prefix": "g4f", "baseUrl": "https://example.test/v1", "protocol": "openai_chat"}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "2", Method: "providers.list"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	items, ok := response.Result.([]store.ProviderNode)
	if !ok || len(items) != 1 {
		t.Fatalf("providers=%#v", response.Result)
	}
}
