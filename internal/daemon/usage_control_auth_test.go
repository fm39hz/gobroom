package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPControlRequiresIndependentTokenBeforeInitialization(t *testing.T) {
	for _, controlToken := range []string{"", "inference-only-key"} {
		root := t.TempDir()
		config := Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), HTTPControl: true, HTTPControlAddr: "127.0.0.1:0", HTTPToken: "inference-only-key", HTTPControlToken: controlToken}
		d := New(config)
		err := d.Start(context.Background())
		if err == nil {
			_ = d.Stop(context.Background())
			t.Fatal("inference token implicitly granted HTTP administration")
		}
		if !strings.Contains(err.Error(), "control token") {
			t.Fatalf("wrong rejection: %v", err)
		}
		if _, err := os.Stat(config.DBPath); !os.IsNotExist(err) {
			t.Fatalf("invalid control config initialized database: %v", err)
		}
	}
}
