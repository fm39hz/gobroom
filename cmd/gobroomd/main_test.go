package main

import (
	"testing"

	"github.com/fm39hz/gobroom/internal/daemon"
)

func TestDaemonConfigPreservesIPCAndSpoolPaths(t *testing.T) {
	config := daemonConfig(daemon.Config{HTTPEnabled: true, RuntimeSpoolMaxBytes: 1024}, "/tmp/gobroom.db", "/tmp/gobroom.spool", "/run/user/1/gobroom.sock")
	if config.DBPath != "/tmp/gobroom.db" || config.RuntimeSpoolPath != "/tmp/gobroom.spool" || config.IPCPath != "/run/user/1/gobroom.sock" || config.RuntimeSpoolMaxBytes != 1024 {
		t.Fatalf("daemon configuration lost command-line paths/defaults: %#v", config)
	}
}
