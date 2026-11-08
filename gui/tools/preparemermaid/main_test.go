package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func archive(t *testing.T, name string, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte("contents")
	if err := tw.WriteHeader(&tar.Header{Name: "root/" + name, Mode: 0644, Size: int64(len(data)), Typeflag: kind}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBoundedSource(t *testing.T) {
	dir := t.TempDir()
	seen, err := extract(archive(t, "src/lib.rs", tar.TypeReg), "root/", dir, nil, false)
	if err != nil || !seen["src/lib.rs"] {
		t.Fatalf("extract: %v, %v", seen, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "src", "lib.rs")); err != nil || string(b) != "contents" {
		t.Fatalf("extracted contents: %q, %v", b, err)
	}
	for _, name := range []string{"../escape", "/absolute", `a\b`, "C:escape"} {
		if _, err := extract(archive(t, name, tar.TypeReg), "root/", t.TempDir(), nil, false); err == nil {
			t.Errorf("unsafe path accepted: %q", name)
		}
	}
}
