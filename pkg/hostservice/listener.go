package hostservice

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/stateversion"

	"golang.org/x/sys/unix"
)

var ErrAlreadyRunning = errors.New("host service already owns this state root")

type Listener struct {
	*net.UnixListener
	lease   *os.File
	version *stateversion.Guard
}

// Listen holds a stable lease before removing an abandoned socket. The lease
// remains on disk so replacement processes cannot lock different file inodes.
func (server *Server) Listen(ctx context.Context) (_ *Listener, result error) {
	version, err := stateversion.AcquireVersionTwo(ctx, server.config.Root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			_ = version.Close()
		}
	}()
	directory, err := version.StateRoot().Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	fd, err := unix.Openat(
		int(directory.Fd()),
		".host.lock",
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0600,
	)
	if err != nil {
		return nil, err
	}
	lease := os.NewFile(uintptr(fd), ".host.lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lease.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	listener, err := listenSocket(filepath.Join(server.config.Root, "host.sock"))
	if err != nil {
		lease.Close()
		return nil, err
	}
	return &Listener{UnixListener: listener, lease: lease, version: version}, nil
}

func listenSocket(path string) (*net.UnixListener, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("host socket path is occupied by a different file")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func (listener *Listener) Close() error {
	err := listener.UnixListener.Close()
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return errors.Join(err, listener.lease.Close(), listener.version.Close())
}
