package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fm39hz/gobroom/internal/daemon"
)

var version = "dev"

func main() {
	defaultDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	defaultDB := filepath.Join(defaultDir, "gobroom", "gobroom.db")

	defaults, err := daemon.DefaultConfig()
	if err != nil {
		log.Fatal(err)
	}
	defaults.DBPath = defaultDB
	dbPath := flag.String("db", defaults.DBPath, "SQLite database path")
	ipcPath := flag.String("ipc", defaults.IPCPath, "Unix socket path")
	addr := flag.String("addr", defaults.HTTPAddr, "HTTP data plane listen address")
	httpEnabled := flag.Bool("http", true, "enable HTTP data plane")
	httpControl := flag.Bool("http-control", false, "expose HTTP control plane")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := daemon.New(daemon.Config{DBPath: *dbPath, IPCPath: *ipcPath, HTTPEnabled: *httpEnabled, HTTPAddr: *addr, HTTPControl: *httpControl})
	if err := d.Start(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("gobroomd ready: ipc=%s http=%v addr=%s", *ipcPath, *httpEnabled, *addr)
	if err := d.Wait(ctx); err != nil {
		log.Fatal(err)
	}
}
