package adb

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	want := Message{Command: CommandCNXN, Arg0: Version, Arg1: MaxPayload, Data: []byte("host::\x00")}
	got, err := ReadMessage(bytes.NewReader(want.Encode()), MaxPayload, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != want.Command || got.Arg0 != want.Arg0 || got.Arg1 != want.Arg1 || !bytes.Equal(got.Data, want.Data) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestMessageChecksumAndMagic(t *testing.T) {
	encoded := (Message{Command: CommandWRTE, Data: []byte("abc")}).Encode()
	encoded[16] ^= 0xff
	if _, err := ReadMessage(bytes.NewReader(encoded), MaxPayload, true); err == nil {
		t.Fatal("expected checksum error")
	}
	encoded = (Message{Command: CommandWRTE, Data: []byte("abc")}).Encode()
	encoded[20] ^= 0xff
	if _, err := ReadMessage(bytes.NewReader(encoded), MaxPayload, false); err == nil {
		t.Fatal("expected magic error")
	}
}

func TestMessageLimits(t *testing.T) {
	if _, err := ReadMessage(bytes.NewReader(nil), MaxPayload, false); !errors.Is(err, io.EOF) {
		t.Fatalf("empty = %v, want EOF", err)
	}
	big := Message{Command: CommandWRTE, Data: make([]byte, MaxPayload+1)}
	if _, err := ReadMessage(bytes.NewReader(big.Encode()), MaxPayload, false); err == nil {
		t.Fatal("expected oversized packet error")
	}
}
