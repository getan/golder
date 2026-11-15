package adb

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestExecStreamRoundTrip(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := ReadMessage(conn, MaxPayload, true); err != nil {
			serverErr <- err
			return
		}
		if _, err := conn.Write((Message{
			Command: CommandCNXN,
			Arg0:    Version,
			Arg1:    4096,
			Data:    []byte("device::\x00"),
		}).Encode()); err != nil {
			serverErr <- err
			return
		}
		open, err := ReadMessage(conn, MaxPayload, true)
		if err != nil {
			serverErr <- err
			return
		}
		if open.Command != CommandOPEN || string(open.Data) != "exec:echo hello\x00" {
			serverErr <- errf("open = %s %q", open.CommandName(), open.Data)
			return
		}
		_, _ = conn.Write((Message{Command: CommandOKAY, Arg0: 7, Arg1: open.Arg0}).Encode())
		write, err := ReadMessage(conn, MaxPayload, true)
		if err != nil {
			serverErr <- err
			return
		}
		if write.Command != CommandWRTE || string(write.Data) != "nonce\n" {
			serverErr <- errf("write = %s %q", write.CommandName(), write.Data)
			return
		}
		_, _ = conn.Write((Message{Command: CommandOKAY, Arg0: 7, Arg1: open.Arg0}).Encode())
		_, _ = conn.Write((Message{Command: CommandWRTE, Arg0: 7, Arg1: open.Arg0, Data: []byte("ok\n")}).Encode())
		if _, err := ReadMessage(conn, MaxPayload, true); err != nil {
			serverErr <- err
			return
		}
		_, _ = conn.Write((Message{Command: CommandCLSE, Arg0: 7, Arg1: open.Arg0}).Encode())
		serverErr <- nil
	}()
	conn := dialTestConn(t, listener)
	defer conn.Close()
	stream, err := conn.Open("exec:echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Write([]byte("nonce\n")); err != nil {
		t.Fatal(err)
	}
	output, err := stream.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, []byte("ok\n")) {
		t.Fatalf("output = %q", output)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func errf(format string, args ...any) error {
	return &stringError{fmt: format, args: args}
}

type stringError struct {
	fmt  string
	args []any
}

func (e *stringError) Error() string {
	return fmt.Sprintf(e.fmt, e.args...)
}
