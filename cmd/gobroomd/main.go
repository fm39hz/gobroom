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
	controlToken := flag.String("control-token", "", "independent bearer token required for HTTP control (or GOBROOM_CONTROL_TOKEN)")
	usageDefaults := daemon.DefaultUsagePolicy()
	usageMaxValues := flag.Int("usage-max-values", usageDefaults.MaxValues, "maximum registered usage values per attempt")
	usageMaxBytes := flag.Int64("usage-max-bytes", usageDefaults.MaxExtensionBytes, "maximum encoded extension usage bytes per attempt")
	usageCaptureRaw := flag.Bool("usage-capture-raw", false, "opt in to bounded decoder-selected raw usage capture")
	usageRawBytes := flag.Int64("usage-max-raw-bytes", usageDefaults.MaxRawBytes, "maximum raw usage fragment bytes")
	usageRetention := flag.Duration("usage-retention", usageDefaults.Retention, "positive local usage history retention")
	usageDetails := flag.Bool("usage-details", false, "allow explicit management usage detail requests")
	usagePrivate := flag.Bool("usage-private-details", false, "separately permit private extension detail requests")
	usageRaw := flag.Bool("usage-raw-details", false, "separately permit raw usage detail requests")
	providerManifestDir := flag.String("provider-manifests", defaults.ProviderManifestDir, "directory containing provider definition JSON files")
	configBundlePath := flag.String("config-bundle", "", "secret-free desired config bundle reapplied before serving on every start")
	quotaPolling := flag.Bool("quota-poll", false, "opt in to periodic provider quota polling")
	usageDrainTimeout := flag.Duration("usage-drain-timeout", defaults.UsageDrainTimeout, "cooperative usage persistence drain budget (0 uses default)")
	artifactStoreDir := flag.String("artifact-store", defaults.ArtifactStoreDir, "parent directory for bounded, temporary artifact body spools")
	artifactStoreMaxBytes := flag.Int64("artifact-store-max-bytes", 256<<20, "maximum total bytes held by the artifact body store")
	artifactStoreMaxBodyBytes := flag.Int64("artifact-store-max-body-bytes", 64<<20, "maximum bytes in one artifact body")
	callbackOrigin := flag.String("callback-origin", "", "public HTTPS origin for proxied authorization callbacks")
	callbackAddr := flag.String("callback-addr", "", "private loopback address for proxied authorization callbacks (host:port)")
	flag.Parse()
	if *providerManifestDir == defaults.ProviderManifestDir {
		if err := os.MkdirAll(*providerManifestDir, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config := daemonConfig(defaults, *dbPath, *runtimeSpoolPath, *ipcPath, *callbackOrigin, *callbackAddr)
	config.HTTPEnabled = *httpEnabled
	config.HTTPAddr = *addr
	config.HTTPControl = *httpControl
	config.HTTPControlAddr = *controlAddr
	config.HTTPToken = *httpToken
	config.HTTPControlToken = *controlToken
	if config.HTTPControlToken == "" {
		config.HTTPControlToken = os.Getenv("GOBROOM_CONTROL_TOKEN")
	}
	config.UsagePolicy = &daemon.UsagePolicy{MaxValues: *usageMaxValues, MaxExtensionBytes: *usageMaxBytes, CaptureRaw: *usageCaptureRaw, MaxRawBytes: *usageRawBytes, Retention: *usageRetention, AllowManagementDetails: *usageDetails, AllowPrivateDetails: *usagePrivate, AllowRawDetails: *usageRaw}
	config.ProviderManifestDir = *providerManifestDir
	config.ConfigBundlePath = *configBundlePath
	config.QuotaPolling = *quotaPolling
	config.UsageDrainTimeout = *usageDrainTimeout
	config.ArtifactStoreDir = *artifactStoreDir
	config.ArtifactStoreMaxBytes = *artifactStoreMaxBytes
	config.ArtifactStoreMaxBodyBytes = *artifactStoreMaxBodyBytes
	d := daemon.New(config)
	if err := d.Start(ctx); err != nil {
		log.Fatal(err)
	}
	addresses := d.Addresses()
	log.Printf("gobroomd ready: ipc=%s http=%v addr=%s control_addr=%s callback_ingress=%s", addresses.IPC, *httpEnabled, addresses.HTTP, addresses.HTTPControl, addresses.CallbackIngress)
	if err := d.Wait(ctx); err != nil {
		log.Fatal(err)
	}
}

func daemonConfig(defaults daemon.Config, dbPath, runtimeSpoolPath, ipcPath, callbackOrigin, callbackAddr string) daemon.Config {
	defaults.DBPath = dbPath
	defaults.RuntimeSpoolPath = runtimeSpoolPath
	defaults.IPCPath = ipcPath
	defaults.CallbackIngress = daemon.AuthorizationCallbackIngressConfig{PublicOrigin: callbackOrigin, ListenAddress: callbackAddr}
	return defaults
}
