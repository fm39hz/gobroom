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
	runtimeSpoolPath := flag.String("runtime-spool", defaults.RuntimeSpoolPath, "durable outbox for runtime health/quota persistence retries")
	ipcPath := flag.String("ipc", defaults.IPCPath, "Unix socket path")
	addr := flag.String("addr", defaults.HTTPAddr, "HTTP data plane listen address")
	httpEnabled := flag.Bool("http", true, "enable HTTP data plane")
	httpControl := flag.Bool("http-control", false, "expose HTTP control plane")
	controlAddr := flag.String("control-addr", defaults.HTTPControlAddr, "HTTP control plane listen address")
	httpToken := flag.String("http-token", "", "optional bearer token for HTTP data plane")
	providerManifestDir := flag.String("provider-manifests", defaults.ProviderManifestDir, "directory containing provider definition JSON files")
	quotaPolling := flag.Bool("quota-poll", false, "opt in to periodic provider quota polling")
	artifactStoreDir := flag.String("artifact-store", defaults.ArtifactStoreDir, "parent directory for bounded, temporary artifact body spools")
	artifactStoreMaxBytes := flag.Int64("artifact-store-max-bytes", 256<<20, "maximum total bytes held by the artifact body store")
	artifactStoreMaxBodyBytes := flag.Int64("artifact-store-max-body-bytes", 64<<20, "maximum bytes in one artifact body")
	flag.Parse()
	if *providerManifestDir == defaults.ProviderManifestDir {
		if err := os.MkdirAll(*providerManifestDir, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config := daemonConfig(defaults, *dbPath, *runtimeSpoolPath, *ipcPath)
	config.HTTPEnabled = *httpEnabled
	config.HTTPAddr = *addr
	config.HTTPControl = *httpControl
	config.HTTPControlAddr = *controlAddr
	config.HTTPToken = *httpToken
	config.ProviderManifestDir = *providerManifestDir
	config.QuotaPolling = *quotaPolling
	config.ArtifactStoreDir = *artifactStoreDir
	config.ArtifactStoreMaxBytes = *artifactStoreMaxBytes
	config.ArtifactStoreMaxBodyBytes = *artifactStoreMaxBodyBytes
	d := daemon.New(config)
	if err := d.Start(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("gobroomd ready: ipc=%s http=%v addr=%s", *ipcPath, *httpEnabled, *addr)
	if err := d.Wait(ctx); err != nil {
		log.Fatal(err)
	}
}

func daemonConfig(defaults daemon.Config, dbPath, runtimeSpoolPath, ipcPath string) daemon.Config {
	defaults.DBPath = dbPath
	defaults.RuntimeSpoolPath = runtimeSpoolPath
	defaults.IPCPath = ipcPath
	return defaults
}
