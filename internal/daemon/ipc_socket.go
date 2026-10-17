package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

var ErrIPCInUse = errors.New("IPC socket is already owned or active")

type ipcSocket struct {
	path     string
	listener *net.UnixListener
	identity os.FileInfo
	lock     *flock.Flock
}

func acquireIPCSocket(ctx context.Context, path string) (*ipcSocket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("IPC path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lockPath := path + ".lock"
	if identity, err := os.Lstat(lockPath); err == nil && !identity.Mode().IsRegular() {
		return nil, fmt.Errorf("IPC lock path %q is not a regular file", lockPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect IPC lock path: %w", err)
	}
	lock := flock.New(lockPath, flock.SetPermissions(0o600))
	locked, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("acquire IPC ownership: %w", err)
	}
	if !locked {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: %s", ErrIPCInUse, path)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	if err := removeStaleIPCSocket(ctx, path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("bind IPC socket: %w", err)
	}
	// Cleanup below checks inode identity; net's automatic unlink would remove
	// any replacement placed at this pathname before Close.
	listener.SetUnlinkOnClose(false)
	identity, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("inspect bound IPC socket: %w", err)
	}
	socket := &ipcSocket{path: path, listener: listener, identity: identity, lock: lock}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = socket.close()
		return nil, fmt.Errorf("set IPC permissions: %w", err)
	}
	keepLock = true
	return socket, nil
}

// The process lock serializes Gobroom owners. Probe existing sockets as well
// so a listener created by another application is never treated as stale.
func removeStaleIPCSocket(ctx context.Context, path string) error {
	identity, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect IPC socket: %w", err)
	}
	if identity.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refuse to replace non-socket IPC path %q", path)
	}
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	connection, dialErr := dialer.DialContext(ctx, "unix", path)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: %s", ErrIPCInUse, path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return fmt.Errorf("probe IPC socket without replacing it: %w", dialErr)
	}
	return removeOwnedIPCSocket(path, identity)
}

func removeOwnedIPCSocket(path string, identity os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(identity, current) || current.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("IPC pathname %q changed ownership; leaving it intact", path)
	}
	return os.Remove(path)
}

func (socket *ipcSocket) stopAccepting() error {
	err := socket.listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (socket *ipcSocket) close() error {
	listenerErr := socket.stopAccepting()
	pathErr := removeOwnedIPCSocket(socket.path, socket.identity)
	// Keep the lock file itself: deleting it can create a second inode and let
	// another process lock the same pathname while an old owner is still alive.
	lockErr := socket.lock.Close()
	return errors.Join(listenerErr, pathErr, lockErr)
}
