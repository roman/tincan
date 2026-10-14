package hostservice

import (
	"errors"
	"net"

	"github.com/roman/tincan/pkg/primary"
	"golang.org/x/sys/unix"
)

func peerCaller(connection *net.UnixConn) (primary.Caller, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return primary.Caller{}, err
	}
	var caller primary.Caller
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if err != nil {
			peerErr = err
			return
		}
		credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			peerErr = err
			return
		}
		if pid < 1 || credentials.Version != 0 {
			peerErr = errors.New("kernel peer identity is unavailable")
			return
		}
		caller = primary.Caller{PID: pid, UID: credentials.Uid}
	})
	if err := errors.Join(err, peerErr); err != nil {
		return primary.Caller{}, err
	}
	return caller, nil
}
