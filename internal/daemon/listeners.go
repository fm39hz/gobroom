package daemon

import (
	"context"
	"fmt"
	"net"
)

const (
	defaultHTTPAddr        = "127.0.0.1:2712"
	defaultHTTPControlAddr = "127.0.0.1:2713"
)

// ListenerAddresses contains the addresses actually owned by a running daemon.
// In particular, a requested TCP port of zero is replaced by the bound port.
type ListenerAddresses struct {
	IPC         string `json:"ipc,omitempty"`
	HTTP        string `json:"http,omitempty"`
	HTTPControl string `json:"httpControl,omitempty"`
}

// Addresses returns an immutable snapshot of the running daemon's listeners.
// It is empty before successful startup and after Stop completes.
func (d *Daemon) Addresses() ListenerAddresses {
	if addresses := d.addresses.Load(); addresses != nil {
		return *addresses
	}
	return ListenerAddresses{}
}

type httpListeners struct {
	data    net.Listener
	control net.Listener
}

func (listeners httpListeners) close() {
	if listeners.data != nil {
		_ = listeners.data.Close()
	}
	if listeners.control != nil {
		_ = listeners.control.Close()
	}
}

// Reserve both ports before initialization publishes IPC or serves requests.
// net/http owns serving and shutdown; this layer only owns bind/rollback.
func reserveHTTPListeners(ctx context.Context, config Config) (httpListeners, error) {
	var listeners httpListeners
	var listenConfig net.ListenConfig
	if config.HTTPEnabled {
		address := config.HTTPAddr
		if address == "" {
			address = defaultHTTPAddr
		}
		listener, err := listenConfig.Listen(ctx, "tcp", address)
		if err != nil {
			return httpListeners{}, fmt.Errorf("bind HTTP data plane: %w", err)
		}
		listeners.data = listener
	}
	if config.HTTPControl {
		address := config.HTTPControlAddr
		if address == "" {
			address = defaultHTTPControlAddr
		}
		listener, err := listenConfig.Listen(ctx, "tcp", address)
		if err != nil {
			listeners.close()
			return httpListeners{}, fmt.Errorf("bind HTTP control plane: %w", err)
		}
		listeners.control = listener
	}
	return listeners, nil
}
