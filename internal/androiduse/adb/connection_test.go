package adb

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDialPlainCNXN(t *testing.T) {
	listener := listen(t)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := ReadMessage(conn, MaxPayload, true); err != nil {
			return
		}
		_, _ = conn.Write((Message{
			Command: CommandCNXN,
			Arg0:    Version,
			Arg1:    4096,
			Data:    []byte("device::\x00"),
		}).Encode())
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Banner != "device::" {
		t.Fatalf("banner = %q, want device::", conn.Banner)
	}
	if conn.MaxData != 4096 {
		t.Fatalf("maxData = %d, want 4096", conn.MaxData)
	}
}

func TestDialSTLS(t *testing.T) {
	cert := testServerCertificate(t)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	listener := listen(t)
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
		if _, err := conn.Write((Message{Command: CommandSTLS, Arg0: STLSVersion}).Encode()); err != nil {
			serverErr <- err
			return
		}
		if _, err := ReadMessage(conn, MaxPayload, true); err != nil {
			serverErr <- err
			return
		}
		secured := tls.Server(conn, cfg)
		if err := secured.Handshake(); err != nil {
			serverErr <- err
			return
		}
		packet := (Message{
			Command: CommandCNXN,
			Arg0:    Version,
			Arg1:    8192,
			Data:    []byte("tls-device::\x00"),
		}).Encode()
		// adbd uses the skip-checksum convention after STLS.
		binary.LittleEndian.PutUint32(packet[16:20], 0)
		_, err = secured.Write(packet)
		serverErr <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientCfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
	conn, err := Dial(ctx, listener.Addr().String(), clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Banner != "tls-device::" || conn.MaxData != 8192 {
		t.Fatalf("unexpected secure banner=%q max=%d", conn.Banner, conn.MaxData)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestConnReadSkipsChecksumForModernVersion(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		packet := (Message{
			Command: CommandCNXN,
			Arg0:    VersionSkipSum,
			Arg1:    4096,
			Data:    []byte("device::\x00"),
		}).Encode()
		binary.LittleEndian.PutUint32(packet[16:20], 0)
		_, _ = server.Write(packet)
	}()
	conn := finishConn(client, Message{Arg0: VersionSkipSum, Arg1: 4096})
	msg, err := conn.Read()
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Data) != "device::\x00" {
		t.Fatalf("payload = %q", msg.Data)
	}
}

func TestDialRejectsLegacyAuth(t *testing.T) {
	listener := listen(t)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = ReadMessage(conn, MaxPayload, true)
		_, _ = conn.Write((Message{Command: CommandAUTH}).Encode())
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err == nil || !strings.Contains(err.Error(), "legacy ADB RSA") {
		t.Fatalf("error = %v, want legacy auth rejection", err)
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func testServerCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "adb-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
