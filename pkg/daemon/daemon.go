// Package daemon exposes the embeddable GoBroom daemon entry points for
// binaries that statically link provider modules through pkg/provider.
package daemon

import (
	"context"

	core "github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

type Config = core.Config
type Daemon = core.Daemon
type IPCRequest = core.IPCRequest
type IPCResponse = core.IPCResponse

func New(config Config) *Daemon { return core.New(config) }

func NewWithProviderModules(config Config, modules ...provider.RuntimeModule) *Daemon {
	return core.NewWithProviderModules(config, modules...)
}

func DefaultConfig() (Config, error) { return core.DefaultConfig() }

func CallIPC(ctx context.Context, path string, request IPCRequest) (IPCResponse, error) {
	return core.CallIPC(ctx, path, request)
}
