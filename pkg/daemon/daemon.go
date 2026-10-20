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
type ListenerAddresses = core.ListenerAddresses
type OperationProbeRequest = core.OperationProbeRequest
type ProbeFileSpec = core.ProbeFileSpec
type CredentialActivationView = core.CredentialActivationView
type AuthorizationSetupView = core.AuthorizationSetupView
type AuthorizationSetupStartMode = core.AuthorizationSetupStartMode
type AuthorizationSetupStartInput = core.AuthorizationSetupStartInput
type AuthorizationCallbackView = core.AuthorizationCallbackView
type AuthorizationCallbackMode = core.AuthorizationCallbackMode

const (
	AuthorizationSetupNew         = core.AuthorizationSetupNew
	AuthorizationSetupResume      = core.AuthorizationSetupResume
	AuthorizationCallbackLoopback = core.AuthorizationCallbackLoopback
	AuthorizationCallbackManual   = core.AuthorizationCallbackManual
)

type CredentialActivationProgress = core.CredentialActivationProgress
type CredentialTransition = core.CredentialTransition

const (
	CredentialRenewal    = core.CredentialRenewal
	PrincipalReplacement = core.PrincipalReplacement
)

var ErrIPCInUse = core.ErrIPCInUse

func New(config Config) *Daemon { return core.New(config) }

func NewWithProviderModules(config Config, modules ...provider.RuntimeModule) *Daemon {
	return core.NewWithProviderModules(config, modules...)
}

func DefaultConfig() (Config, error) { return core.DefaultConfig() }

func CallIPC(ctx context.Context, path string, request IPCRequest) (IPCResponse, error) {
	return core.CallIPC(ctx, path, request)
}

func CallOperationProbe(ctx context.Context, path string, request OperationProbeRequest, files []ProbeFileSpec) (IPCResponse, error) {
	return core.CallOperationProbe(ctx, path, request, files)
}
