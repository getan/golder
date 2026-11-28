//go:build windows

package browserctl

import (
	"context"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialControl(ctx context.Context, endpoint string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := 3 * time.Second
	conn, err := winio.DialPipe(endpoint, &timeout)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
