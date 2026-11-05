//go:build linux

package computeruse

import (
	"errors"
	"fmt"
	"net"
	"os"

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
	var credential *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if socketErr != nil || credential == nil || credential.Pid <= 0 || credential.Uid != uint32(os.Getuid()) {
		return 0, 0, errors.New("computer pane peer credentials unavailable")
	}
	pid := int(credential.Pid)
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, errors.New("computer pane peer process unavailable")
	}
	start, err := linuxProcessStart(string(data), pid)
	if err != nil {
		return 0, 0, err
	}
	return pid, start, nil
}
