package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyModule(t *testing.T) {
	data := []byte("example wasm")
	sum := sha256.Sum256(data)
	if err := verify(data, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err := verify(data, "wrong"); err == nil {
		t.Fatal("accepted an unverified module")
	}
	if err := verify(nil, "wrong"); err == nil {
		t.Fatal("accepted an empty module")
	}
}
