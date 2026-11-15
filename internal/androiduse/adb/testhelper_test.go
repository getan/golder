package adb

import (
	"context"
	"net"
	"testing"
	"time"
)

func dialTestConn(t *testing.T, listener net.Listener) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, err := Dial(ctx, listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
