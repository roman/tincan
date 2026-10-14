package hostservice

import (
	"errors"
	"github.com/roman/tincan/pkg/primary"
	"golang.org/x/sys/unix"
	"net"
)

func peerCaller(connection *net.UnixConn) (primary.Caller, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return primary.Caller{}, err
	}
	var credentials *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		credentials, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err := errors.Join(err, peerErr); err != nil {
		return primary.Caller{}, err
	}
	return primary.Caller{PID: int(credentials.Pid), UID: credentials.Uid}, nil
}
