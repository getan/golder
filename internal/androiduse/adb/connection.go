package adb

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Conn struct {
	conn     net.Conn
	Banner   string
	Version  uint32
	MaxData  uint32
	streamMu sync.Mutex
	nextID   uint32
}

// Dial performs the ADB CNXN/STLS handshake. tlsConfig is only used when the
// device requests STLS; the caller is responsible for pinning the device
// certificate and presenting the paired client certificate.
func Dial(ctx context.Context, address string, tlsConfig *tls.Config) (*Conn, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial adb: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()

	host := Message{
		Command: CommandCNXN,
		Arg0:    Version,
		Arg1:    MaxPayload,
		Data:    []byte("host::\x00"),
	}
	if _, err := raw.Write(host.Encode()); err != nil {
		return nil, fmt.Errorf("write adb CNXN: %w", err)
	}
	first, err := ReadMessage(raw, MaxPayload, true)
	if err != nil {
		return nil, fmt.Errorf("read adb greeting: %w", err)
	}
	switch first.Command {
	case CommandCNXN:
		conn := finishConn(raw, first)
		ok = true
		return conn, nil
	case CommandSTLS:
		if tlsConfig == nil {
			return nil, fmt.Errorf("adb requested TLS but no TLS configuration was supplied")
		}
		stls := Message{Command: CommandSTLS, Arg0: STLSVersion}
		if _, err := raw.Write(stls.Encode()); err != nil {
			return nil, fmt.Errorf("write adb STLS: %w", err)
		}
		secured := tls.Client(raw, tlsConfig)
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("adb TLS handshake: %w", err)
		}
		cnxn, err := ReadMessage(secured, MaxPayload, true)
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("adb closed after TLS (client certificate not paired?)")
			}
			return nil, fmt.Errorf("read adb CNXN after TLS: %w", err)
		}
		if cnxn.Command != CommandCNXN {
			return nil, fmt.Errorf("expected adb CNXN, got %s", cnxn.CommandName())
		}
		conn := finishConn(secured, cnxn)
		ok = true
		return conn, nil
	case CommandAUTH:
		return nil, fmt.Errorf("device requested legacy ADB RSA authentication; this is not a wireless-debugging port")
	default:
		return nil, fmt.Errorf("unexpected adb greeting: %s", first.CommandName())
	}
}

func finishConn(conn net.Conn, cnxn Message) *Conn {
	maxData := cnxn.Arg1
	if maxData == 0 || maxData > MaxPayload {
		maxData = DefaultMaxPayload
	}
	return &Conn{
		conn:    conn,
		Banner:  trimNUL(cnxn.Data),
		Version: cnxn.Arg0,
		MaxData: maxData,
		nextID:  1,
	}
}

func trimNUL(data []byte) string {
	for i, b := range data {
		if b == 0 {
			return string(data[:i])
		}
	}
	return string(data)
}

func (c *Conn) Send(message Message) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("adb connection is closed")
	}
	_, err := c.conn.Write(message.Encode())
	if err != nil {
		return fmt.Errorf("write adb %s: %w", message.CommandName(), err)
	}
	return nil
}

func (c *Conn) Read() (Message, error) {
	if c == nil || c.conn == nil {
		return Message{}, fmt.Errorf("adb connection is closed")
	}
	return ReadMessage(c.conn, MaxPayload, true)
}

func (c *Conn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
