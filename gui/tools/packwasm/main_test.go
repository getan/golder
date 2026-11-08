package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackWASM(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.wasm"), filepath.Join(dir, "out.wasm.gz")
	data := append([]byte("\x00asm\x01\x00\x00\x00"), bytes.Repeat([]byte("abc"), 1000)...)
	if err := os.WriteFile(in, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := pack(in, out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("roundtrip differs: %v", err)
	}
	if err := os.WriteFile(in, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := pack(in, out); err == nil {
		t.Fatal("invalid input accepted")
	}
}
