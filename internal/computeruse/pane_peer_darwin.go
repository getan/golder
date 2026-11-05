//go:build darwin

package computeruse

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

func operatorPeerIdentity(conn net.Conn) (int, uint64, error) {
	peer, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, 0, errors.New("computer pane requires a Unix socket")
	}
	raw, err := peer.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var pid int
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, 0, err
	}
	if socketErr != nil || pid <= 0 {
		return 0, 0, errors.New("computer pane peer PID unavailable")
	}
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || proc == nil || int(proc.Proc.P_pid) != pid {
		return 0, 0, errors.New("computer pane peer process unavailable")
	}
	tv := proc.Proc.P_starttime
	if tv.Sec <= 0 || tv.Usec < 0 {
		return 0, 0, errors.New("computer pane peer start time unavailable")
	}
	return pid, uint64(tv.Sec)*1_000_000 + uint64(tv.Usec), nil
}
