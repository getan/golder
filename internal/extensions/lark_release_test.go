package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testLarkArchive(t *testing.T, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gz)
	content := []byte("fixture helper binary")
	h := &tar.Header{Name: "golder-lark", Mode: 0o700, Typeflag: kind}
	if kind == tar.TypeReg {
		h.Size = int64(len(content))
	}
	if err := tarWriter.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLarkReleaseInstallVerifiesArchive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	archive := testLarkArchive(t, tar.TypeReg)
	digest := sha256.Sum256(archive)
	var checksum string
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			_, _ = fmt.Fprint(w, checksum)
		} else {
			_, _ = w.Write(archive)
		}
	}))
	defer server.Close()
	release, err := larkRelease("v1.2.3", "darwin", "arm64", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	checksum = fmt.Sprintf("%x  %s\n", digest, release.Archive)
	pkg, err := installLarkRelease(context.Background(), release, server.Client())
	if err != nil || pkg.Version != "1.2.3" {
		t.Fatalf("verified install failed: %v", err)
	}
	if len(requested) != 2 || !strings.HasSuffix(requested[0], "checksums.txt") {
		t.Fatal("did not fetch checksum before archive")
	}
	if info, err := os.Stat(pkg.Executable); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("managed helper is not private: %v", err)
	}
	checksum = strings.Repeat("0", 64) + "  " + release.Archive + "\n"
	if _, err := installLarkRelease(context.Background(), release, server.Client()); err == nil {
		t.Fatal("accepted a mismatched archive checksum")
	}
}

func TestLarkReleaseRejectsUntrustedInputAndSymlink(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	for _, version := range []string{"dev", "v1.2.3/../../other", "1.2.3-rc1", ""} {
		if _, err := larkRelease(version, "darwin", "arm64", "https://github.com/getan/golder/releases/download"); err == nil {
			t.Fatalf("accepted invalid version %q", version)
		}
	}
	archive := testLarkArchive(t, tar.TypeSymlink)
	digest := sha256.Sum256(archive)
	var release LarkRelease
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			_, _ = fmt.Fprintf(w, "%x  %s\n", digest, release.Archive)
		} else {
			_, _ = w.Write(archive)
		}
	}))
	defer server.Close()
	release, _ = larkRelease("1.2.3", "darwin", "arm64", server.URL)
	if _, err := installLarkRelease(context.Background(), release, server.Client()); err == nil {
		t.Fatal("accepted a symlink as the Lark executable")
	}
}
