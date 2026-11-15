package adb

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

type PeerInfo struct {
	Type int
	Data string
}

const (
	pairingVersion = byte(1)
	pairingSpake2  = byte(0)
	pairingInfo    = byte(1)
	peerInfoSize   = 8192
)

// Pair performs the wireless-debugging TLS/SPAKE2 pairing exchange. The
// device's self-signed certificate is TOFU at this stage; callers should pin
// the post-pairing certificate for subsequent ADB connections.
func Pair(ctx context.Context, address string, identity *Identity, code string) (PeerInfo, error) {
	if identity == nil {
		return PeerInfo{}, fmt.Errorf("pairing identity is required")
	}
	if code == "" {
		return PeerInfo{}, fmt.Errorf("pairing code is required")
	}
	cert, err := identity.TLSCertificate()
	if err != nil {
		return PeerInfo{}, err
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return PeerInfo{}, fmt.Errorf("dial pairing port: %w", err)
	}
	defer raw.Close()
	cfg := &tls.Config{
		Certificates:           []tls.Certificate{cert},
		InsecureSkipVerify:     true, // pairing uses TOFU; the code authenticates the exchange
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
	}
	secured := tls.Client(raw, cfg)
	if err := secured.HandshakeContext(ctx); err != nil {
		return PeerInfo{}, fmt.Errorf("pairing TLS handshake: %w", err)
	}
	state := secured.ConnectionState()
	exported, err := state.ExportKeyingMaterial("adb-label\x00", nil, 64)
	if err != nil {
		return PeerInfo{}, fmt.Errorf("export pairing keying material: %w", err)
	}
	codeMaterial := append([]byte(code), exported...)
	seed := make([]byte, 64)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return PeerInfo{}, err
	}
	auth, err := NewPairingAuth(SpakeAlice, codeMaterial, seed)
	if err != nil {
		return PeerInfo{}, err
	}
	if err := writePairPacket(secured, pairingSpake2, auth.Message); err != nil {
		return PeerInfo{}, err
	}
	theirMessage, err := readPairPacket(secured, pairingSpake2)
	if err != nil {
		return PeerInfo{}, err
	}
	if err := auth.Init(theirMessage); err != nil {
		return PeerInfo{}, err
	}
	info := make([]byte, peerInfoSize)
	keyLine := identity.ADBPublicKey()
	if len(keyLine) >= peerInfoSize-1 {
		return PeerInfo{}, fmt.Errorf("ADB public key is too long")
	}
	info[0] = 0
	copy(info[1:], keyLine)
	sealed, err := auth.Encrypt(info)
	if err != nil {
		return PeerInfo{}, err
	}
	if err := writePairPacket(secured, pairingInfo, sealed); err != nil {
		return PeerInfo{}, err
	}
	sealedPeer, err := readPairPacket(secured, pairingInfo)
	if err != nil {
		return PeerInfo{}, err
	}
	peer, err := auth.Decrypt(sealedPeer)
	if err != nil {
		return PeerInfo{}, fmt.Errorf("decrypt paired device info: %w", err)
	}
	if len(peer) != peerInfoSize {
		return PeerInfo{}, fmt.Errorf("peer info has %d bytes, want %d", len(peer), peerInfoSize)
	}
	end := 1
	for end < len(peer) && peer[end] != 0 {
		end++
	}
	return PeerInfo{Type: int(peer[0]), Data: strings.TrimSpace(string(peer[1:end]))}, nil
}

func writePairPacket(w io.Writer, kind byte, payload []byte) error {
	header := make([]byte, 6)
	header[0] = pairingVersion
	header[1] = kind
	binary.BigEndian.PutUint32(header[2:], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write pairing header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write pairing payload: %w", err)
	}
	return nil
}

func readPairPacket(r io.Reader, want byte) ([]byte, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read pairing header: %w", err)
	}
	if header[0] != pairingVersion {
		return nil, fmt.Errorf("unsupported pairing version %d", header[0])
	}
	if header[1] != want {
		return nil, fmt.Errorf("unexpected pairing packet type %d", header[1])
	}
	length := binary.BigEndian.Uint32(header[2:])
	if length == 0 || length > peerInfoSize*2 {
		return nil, fmt.Errorf("invalid pairing payload length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read pairing payload: %w", err)
	}
	return payload, nil
}
