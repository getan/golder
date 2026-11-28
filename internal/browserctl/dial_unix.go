//go:build !windows

package browserctl

import (
	"context"
	"net"
	"time"
)

func dialControl(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	return dialer.DialContext(ctx, "unix", endpoint)
}
