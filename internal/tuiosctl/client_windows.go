//go:build windows

package tuiosctl

import (
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// KernelPeerPID returns the process on the other end of this exact local
// daemon connection. A socket path or daemon-supplied PID is not proof.
func (c *Client) KernelPeerPID() (uint32, error) {
	if c == nil {
		return 0, errors.New("tuios daemon connection unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	peer, ok := c.conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("tuios daemon is not a local Unix socket")
	}
	raw, err := peer.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid, returned uint32
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		const sioAFUnixGetPeerPID = 0x58000100
		socketErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &returned, nil, 0)
	}); err != nil {
		return 0, err
	}
	if socketErr != nil || returned != uint32(unsafe.Sizeof(pid)) || pid == 0 {
		return 0, errors.New("tuios daemon peer PID unavailable")
	}
	return pid, nil
}
