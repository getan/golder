//go:build darwin

package computeruse

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorPeerIdentityUsesKernelPIDAndStartTime(t *testing.T) {
	dir, err := os.MkdirTemp("", "cu-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dir)
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(path)
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pid, start, err := operatorPeerIdentity(conn)
	if err != nil || pid != os.Getpid() || start == 0 {
		t.Fatalf("peer identity pid=%d start=%d err=%v", pid, start, err)
	}
}
