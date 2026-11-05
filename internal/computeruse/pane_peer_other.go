//go:build !darwin && !linux && !windows

package computeruse

import "net"

// Until a platform can pin the peer process and its start time, no pane may
// claim operator authority through an IPC connection.
func operatorPeerIdentity(net.Conn) (int, uint64, error) {
	return 0, 0, ErrUnavailable
}
