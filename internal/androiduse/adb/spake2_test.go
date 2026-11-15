package adb

import (
	"bytes"
	"testing"
)

func TestSpake2RoundTrip(t *testing.T) {
	code := []byte("123456")
	seedA := bytes.Repeat([]byte{0x11}, 64)
	seedB := bytes.Repeat([]byte{0x22}, 64)
	alice := NewSpake2(SpakeAlice, pairingClientName, pairingServerName)
	bob := NewSpake2(SpakeBob, pairingServerName, pairingClientName)
	amsg, err := alice.Generate(code, seedA)
	if err != nil {
		t.Fatal(err)
	}
	bmsg, err := bob.Generate(code, seedB)
	if err != nil {
		t.Fatal(err)
	}
	akey, err := alice.Process(bmsg)
	if err != nil {
		t.Fatal(err)
	}
	bkey, err := bob.Process(amsg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(akey, bkey) {
		t.Fatal("SPAKE2 shared keys differ")
	}
}

func TestPairingCipherRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 64)
	a, err := newPairingCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newPairingCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed := a.Encrypt([]byte("hello"))
	got, err := b.Decrypt(sealed)
	if err != nil || string(got) != "hello" {
		t.Fatalf("got %q, %v", got, err)
	}
}
