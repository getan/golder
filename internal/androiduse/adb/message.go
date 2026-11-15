package adb

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	CommandCNXN = 0x4e584e43
	CommandAUTH = 0x48545541
	CommandOPEN = 0x4e45504f
	CommandOKAY = 0x59414b4f
	CommandCLSE = 0x45534c43
	CommandWRTE = 0x45545257
	CommandSTLS = 0x534c5453

	Version           = 0x01000001
	VersionSkipSum    = 0x01000001
	STLSVersion       = 0x01000000
	HeaderSize        = 24
	MaxPayload        = 1024 * 1024
	DefaultMaxPayload = 4096
)

type Message struct {
	Command uint32
	Arg0    uint32
	Arg1    uint32
	Data    []byte
}

func (m Message) Encode() []byte {
	out := make([]byte, HeaderSize+len(m.Data))
	binary.LittleEndian.PutUint32(out[0:4], m.Command)
	binary.LittleEndian.PutUint32(out[4:8], m.Arg0)
	binary.LittleEndian.PutUint32(out[8:12], m.Arg1)
	binary.LittleEndian.PutUint32(out[12:16], uint32(len(m.Data)))
	binary.LittleEndian.PutUint32(out[16:20], Checksum(m.Data))
	binary.LittleEndian.PutUint32(out[20:24], ^m.Command)
	copy(out[HeaderSize:], m.Data)
	return out
}

func Checksum(data []byte) uint32 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return sum
}

// ReadMessage reads one 24-byte-header ADB packet. A clean EOF before the
// header returns io.EOF; a truncated header/data is io.ErrUnexpectedEOF.
func ReadMessage(r io.Reader, maxData int, verifyChecksum bool) (Message, error) {
	var m Message
	if maxData <= 0 {
		maxData = MaxPayload
	}
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if err == io.EOF {
			return m, io.EOF
		}
		return m, fmt.Errorf("read adb header: %w", err)
	}
	m.Command = binary.LittleEndian.Uint32(header[0:4])
	m.Arg0 = binary.LittleEndian.Uint32(header[4:8])
	m.Arg1 = binary.LittleEndian.Uint32(header[8:12])
	length := binary.LittleEndian.Uint32(header[12:16])
	sum := binary.LittleEndian.Uint32(header[16:20])
	magic := binary.LittleEndian.Uint32(header[20:24])
	if magic != ^m.Command {
		return m, fmt.Errorf("bad adb packet magic for %s", m.CommandName())
	}
	if length > uint32(maxData) {
		return m, fmt.Errorf("adb packet too large: %d", length)
	}
	if length > 0 {
		m.Data = make([]byte, length)
		if _, err := io.ReadFull(r, m.Data); err != nil {
			return m, fmt.Errorf("read adb payload: %w", err)
		}
	}
	if verifyChecksum && Checksum(m.Data) != sum {
		return m, fmt.Errorf("bad adb packet checksum")
	}
	return m, nil
}

func (m Message) CommandName() string {
	b := []byte{
		byte(m.Command),
		byte(m.Command >> 8),
		byte(m.Command >> 16),
		byte(m.Command >> 24),
	}
	for _, c := range b {
		if c < 'A' || c > 'Z' {
			return fmt.Sprintf("0x%08x", m.Command)
		}
	}
	return string(b)
}
