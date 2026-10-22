package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestDaemonStopDrainsAcceptedUsageBeforeClosingStore(t *testing.T) {
	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "run", "daemon.sock")}
	d := New(config)
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Stop(context.Background()) }()
	const count = 128
	for index := range count {
		if !d.kernel.EmitUsage(kernel.UsageEvent{At: time.Now(), LogicalModel: fmt.Sprintf("accepted-%03d", index), Status: "error", InputTokens: new(int64(3)), OutputTokens: new(int64(2))}) {
			t.Fatalf("fixture queue did not accept event %d", index)
		}
	}
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, cli, "--ipc", config.IPCPath, "shutdown").CombinedOutput(); err != nil {
			t.Fatalf("installed shutdown: %v (%s)", err, output)
		}
		if err := d.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	} else if err := d.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(config.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.UsageEvents(count)
	if err != nil || len(rows) != count {
		t.Fatalf("drained rows=%d want%d err=%v", len(rows), count, err)
	}
	for index, row := range rows {
		if row.LogicalModel != fmt.Sprintf("accepted-%03d", count-index-1) || row.Status != "error" || row.InputTokens == nil || *row.InputTokens != 3 || row.OutputTokens == nil || *row.OutputTokens != 2 {
			t.Fatalf("lost or changed usage row %d: %#v", index, row)
		}
	}
}
