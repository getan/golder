package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveName(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "golder_0.4.0_Darwin_arm64.tar.gz"},
		{"darwin", "amd64", "golder_0.4.0_Darwin_x86_64.tar.gz"},
		{"linux", "amd64", "golder_0.4.0_Linux_x86_64.tar.gz"},
		{"linux", "386", "golder_0.4.0_Linux_i386.tar.gz"},
		{"windows", "amd64", "golder_0.4.0_Windows_x86_64.zip"},
	}
	for _, tt := range tests {
		u := &Updater{GOOS: tt.goos, GOARCH: tt.goarch}
		if got := u.archiveName("0.4.0"); got != tt.want {
			t.Errorf("archiveName(%s/%s) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("abc123  golder_0.4.0_Linux_x86_64.tar.gz\ndef456  golder_0.4.0_Darwin_arm64.tar.gz\n")
	got, err := checksumFor(sums, "golder_0.4.0_Darwin_arm64.tar.gz")
	if err != nil || got != "def456" {
		t.Errorf("checksumFor = (%q,%v), want (def456,nil)", got, err)
	}
	if _, err := checksumFor(sums, "missing.tar.gz"); err == nil {
		t.Error("expected error for missing archive")
	}
}

func TestExtractBinaryTarGz(t *testing.T) {
	want := []byte("#!fake golder binary")
	archive := makeTarGz(t, "golder", want)
	got, err := extractBinary(archive, "golder", false)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted = %q, want %q", got, want)
	}
	if _, err := extractBinary(archive, "nope", false); err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestReplaceAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &Updater{ExecPath: target}
	newBin := []byte("new binary content")
	if err := u.replace(newBin); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, newBin) {
		t.Errorf("after replace = %q, want %q", got, newBin)
	}
	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected 1 file after replace, got %d", len(entries))
	}
}

func TestApplyEndToEnd(t *testing.T) {
	binary := []byte("brand new golder v0.4.0")
	archive := makeTarGz(t, "golder", binary)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(archive)
		case checksumsFile:
			_, _ = w.Write([]byte(sums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	u := &Updater{
		HTTPClient:     srv.Client(),
		Repo:           "getan/golder",
		ReleaseBaseURL: srv.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, binary) {
		t.Errorf("target after Apply = %q, want %q", got, binary)
	}
}

func TestApplyChecksumMismatch(t *testing.T) {
	archive := makeTarGz(t, "golder", []byte("real content"))
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	// Wrong checksum on purpose.
	sums := "0000000000000000000000000000000000000000000000000000000000000000  " + archiveName + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(archive)
		case checksumsFile:
			_, _ = w.Write([]byte(sums))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	u := &Updater{
		HTTPClient:     srv.Client(),
		ReleaseBaseURL: srv.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &bytes.Buffer{}); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	// Target must be untouched on checksum failure.
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("target modified despite checksum failure: %q", got)
	}
}

func TestMirrorPrefixes(t *testing.T) {
	t.Setenv(mirrorEnv, "")
	if got := mirrorPrefixes(); len(got) != len(defaultMirrorPrefixes) {
		t.Errorf("unset = %v, want defaults %v", got, defaultMirrorPrefixes)
	}
	t.Setenv(mirrorEnv, "off")
	if got := mirrorPrefixes(); got != nil {
		t.Errorf("off = %v, want nil", got)
	}
	t.Setenv(mirrorEnv, "none")
	if got := mirrorPrefixes(); got != nil {
		t.Errorf("none = %v, want nil", got)
	}
	t.Setenv(mirrorEnv, "https://a.example/  https://b.example/")
	got := mirrorPrefixes()
	if len(got) != 2 || got[0] != "https://a.example/" || got[1] != "https://b.example/" {
		t.Errorf("custom = %v, want [https://a.example/ https://b.example/]", got)
	}
}

// TestApplyFallsBackToMirror covers the mainland-China case: the direct source
// errors for checksums and connects-then-stalls for the archive; the mirror
// serves both and the install succeeds.
func TestApplyFallsBackToMirror(t *testing.T) {
	binary := []byte("new binary via mirror")
	archive := makeTarGz(t, "golder", binary)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == checksumsFile {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer direct.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(archive)
		case checksumsFile:
			_, _ = w.Write([]byte(sums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mirror.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:     direct.Client(),
		Repo:           "getan/golder",
		ReleaseBaseURL: direct.URL,
		Mirrors:        []string{mirror.URL + "/"},
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
		StallTimeout:   150 * time.Millisecond,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, binary) {
		t.Errorf("target = %q, want %q", got, binary)
	}
	if !strings.Contains(out.String(), "trying") {
		t.Errorf("expected a fallback notice, got:\n%s", out.String())
	}
}

// TestApplyFallsBackOnChecksumMismatch: the direct source serves a corrupt
// archive; the digest check rejects it and the next source is tried.
func TestApplyFallsBackOnChecksumMismatch(t *testing.T) {
	good := makeTarGz(t, "golder", []byte("good binary"))
	corrupt := makeTarGz(t, "golder", []byte("corrupt binary"))
	sum := sha256.Sum256(good)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(corrupt)
		case checksumsFile:
			_, _ = w.Write([]byte(sums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer direct.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == archiveName {
			_, _ = w.Write(good)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mirror.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:     direct.Client(),
		ReleaseBaseURL: direct.URL,
		Mirrors:        []string{mirror.URL + "/"},
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, []byte("good binary")) {
		t.Errorf("target = %q, want %q", got, "good binary")
	}
	if !strings.Contains(out.String(), "sha256 mismatch") {
		t.Errorf("expected a mismatch notice, got:\n%s", out.String())
	}
}

func makeTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
