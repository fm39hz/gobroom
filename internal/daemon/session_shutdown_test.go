package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
)

func TestStopJoinsSessionFailureFirstObservedDuringShutdown(t *testing.T) {
	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock")}
	d := New(config)
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	stopped := false
	defer func() {
		if !stopped {
			_ = d.Stop(context.Background())
		}
	}()

	callIPC := func(method string, params map[string]any, result any) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := CallIPC(ctx, config.IPCPath, IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("IPC %s: %v / %s", method, err, response.Error)
		}
		data, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, result); err != nil {
			t.Fatal(err)
		}
	}
	definition, ok := d.providerRegistry.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		t.Fatal("missing built-in chat provider definition")
	}
	var node, connection struct{ ID string }
	callIPC("providers.create", map[string]any{"name": "Shutdown fixture", "prefix": "shutdown-fixture", "baseUrl": "https://provider.test/v1", "protocol": "openai_chat", "definitionID": definition.ID}, &node)
	callIPC("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Fixture", "credentialType": "api_key", "secret": "shutdown-fixture-key"}, &connection)

	providerNode, err := d.store.ProviderNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	credential, ok := d.store.ConnectionCredentialByID(connection.ID)
	if !ok {
		t.Fatal("created connection has no credential")
	}
	if err := credential.Owner.Validate(); err != nil {
		t.Fatalf("created credential owner: %v", err)
	}
	route := kernel.Route{
		ID: "shutdown-fixture-route", NodeID: providerNode.ID, DefinitionID: credential.Owner.Definition.ID,
		DefinitionRef: credential.Owner.Definition, BaseURL: providerNode.BaseURL, Protocol: kernel.Protocol(providerNode.Protocol),
		ExternalModel: "fixture-model", CredentialID: credential.ID, CredentialType: credential.Type,
		SessionOperationRef: extensions.Ref{Kind: "operation", ID: string(normalize.OperationChatGenerate), ContractVersion: 1},
		HealthGeneration:    kernel.RouteHealthGeneration{Connection: credential.OperationalGeneration()},
	}
	if _, err := d.store.DB.Exec("CREATE TRIGGER reject_shutdown_session_write BEFORE INSERT ON provider_sessions BEGIN SELECT RAISE(FAIL,'shutdown fixture failure'); END"); err != nil {
		t.Fatal(err)
	}

	writer, err := d.store.DB.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writerOpen := true
	defer func() {
		if writerOpen {
			_ = writer.Rollback()
		}
	}()
	if _, err := writer.Exec("UPDATE provider_nodes SET name=name WHERE id=?", node.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.sessionCache.Save(context.Background(), route, "shutdown-client-session", kernel.SessionState{ResponseID: "shutdown-response"}); err != nil {
		t.Fatal(err)
	}

	observeCtx, cancelObserve := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelObserve()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status := d.sessionCache.PersistenceStatus()
		if status.Received == 1 {
			if status.Failed != 0 {
				t.Fatalf("failure occurred before shutdown: %+v", status)
			}
			break
		}
		select {
		case <-observeCtx.Done():
			t.Fatalf("worker did not receive queued write: %+v", status)
		case <-ticker.C:
		}
	}

	d.mu.Lock()
	runCtx := d.runCtx
	d.mu.Unlock()
	stopDone := make(chan error, 1)
	go func() { stopDone <- d.Stop(context.Background()) }()
	select {
	case <-runCtx.Done():
	case <-observeCtx.Done():
		t.Fatal("Stop did not begin before the observation deadline")
	}
	if status := d.sessionCache.PersistenceStatus(); status.Received != 1 || status.Failed != 0 {
		t.Fatalf("shutdown write failed before releasing SQLite writer: %+v", status)
	}
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned while the session write was blocked: %v", err)
	default:
	}

	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	writerOpen = false

	var stopErr error
	select {
	case stopErr = <-stopDone:
	case <-observeCtx.Done():
		t.Fatal("Stop did not join the session worker")
	}
	stopped = true
	if !errors.Is(stopErr, ErrSessionPersistenceFailureObserved) {
		t.Fatalf("Stop error = %v, want session persistence failure sentinel", stopErr)
	}
	status := d.sessionCache.PersistenceStatus()
	if status.Received != 1 || status.Failed != 1 {
		t.Fatalf("shutdown persistence status = %+v, want one received and one failed", status)
	}
	if err := d.Stop(context.Background()); !errors.Is(err, ErrSessionPersistenceFailureObserved) {
		t.Fatalf("repeated Stop lost session failure: %v", err)
	}
	if err := d.Wait(context.Background()); !errors.Is(err, ErrSessionPersistenceFailureObserved) {
		t.Fatalf("Wait lost session failure: %v", err)
	}
}
