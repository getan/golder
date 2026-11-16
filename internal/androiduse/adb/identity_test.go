package adb

import (
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityPublicKeyFormat(t *testing.T) {
	identity, err := GenerateIdentity("golder test")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := base64.StdEncoding.DecodeString(strings.Split(identity.ADBPublicKey(), " ")[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 524 {
		t.Fatalf("public key blob = %d, want 524", len(blob))
	}
	if got := binaryLE(blob[0:4]); got != 64 {
		t.Fatalf("modulus word count = %d, want 64", got)
	}
	if got := binaryLE(blob[520:524]); got != 65537 {
		t.Fatalf("exponent = %d, want 65537", got)
	}
	if !strings.HasSuffix(identity.ADBPublicKey(), "golder_test") {
		t.Fatalf("name not sanitized: %q", identity.ADBPublicKey())
	}
	modulus := append([]byte(nil), blob[8:264]...)
	reverseBytes(modulus)
	n := new(big.Int).SetBytes(modulus)
	if n.Cmp(identity.Private.N) != 0 {
		t.Fatal("encoded modulus does not match key")
	}
	rrBytes := append([]byte(nil), blob[264:520]...)
	reverseBytes(rrBytes)
	wantRR := new(big.Int).Exp(new(big.Int).Lsh(big.NewInt(1), 2048), big.NewInt(2), n)
	if new(big.Int).SetBytes(rrBytes).Cmp(wantRR) != 0 {
		t.Fatal("encoded rr does not match Montgomery constant")
	}
	n0 := new(big.Int).And(n, new(big.Int).SetUint64(1<<32-1)).Uint64()
	n0inv := uint64(binaryLE(blob[4:8]))
	if (n0*n0inv+1)&0xffffffff != 0 {
		t.Fatalf("n0inv does not invert n0: n0=%x n0inv=%x", n0, n0inv)
	}
}

func TestIdentityPersistsAndDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adb", "private.pem")
	first, err := LoadOrCreateIdentity(path, "golder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateIdentity(path, "golder")
	if err != nil {
		t.Fatal(err)
	}
	if first.Private.N.Cmp(second.Private.N) != 0 {
		t.Fatal("identity changed after reload")
	}
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(path, "golder"); err == nil {
		t.Fatal("corrupt key must fail rather than being replaced")
	}
	if got, _ := os.ReadFile(path); string(got) != "not a key" {
		t.Fatal("corrupt key was overwritten")
	}
}

func TestEncodeRejectsOversizedRSAKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for oversized key")
		}
	}()
	EncodeRSAPublicKey(&rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 2049), E: 65537})
}

func binaryLE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}
