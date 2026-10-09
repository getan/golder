package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
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
		RateWindow:     150 * time.Millisecond,
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

// TestApplyFallsBackOnSlowTrickle covers the "connected but crawls" case the
// silence rule alone would let through: the direct source delivers a few
// hundred bytes and keeps trickling far below the floor, so the attempt must
// be aborted within a window and the mirror must serve the archive.
func TestApplyFallsBackOnSlowTrickle(t *testing.T) {
	binary := []byte("new binary via mirror after trickle")
	archive := makeTarGz(t, "golder", binary)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == checksumsFile {
			_, _ = w.Write([]byte(sums))
			return
		}
		w.WriteHeader(http.StatusOK)
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		flush()
		small := make([]byte, 32)
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline:
				// Safety valve: if the rate watchdog failed to abort us, stop
				// trickling so the test fails on the notice instead of hanging
				// (the truncated body then fails its checksum).
				return
			case <-time.After(50 * time.Millisecond):
				if _, err := w.Write(small); err != nil {
					return
				}
				flush()
			}
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
		HTTPClient:         direct.Client(),
		ReleaseBaseURL:     direct.URL,
		Mirrors:            []string{mirror.URL + "/"},
		GOOS:               "linux",
		GOARCH:             "amd64",
		ExecPath:           target,
		MinRateBytesPerSec: 64 * 1024,
		RateWindow:         200 * time.Millisecond,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, binary) {
		t.Errorf("target = %q, want %q", got, binary)
	}
	if !strings.Contains(out.String(), "too slow") {
		t.Errorf("expected a rate-floor notice, got:\n%s", out.String())
	}
}

// TestApplyKeepsSourceAboveRateFloor: a source that delivers slowly but stays
// above the floor (here ~20 KB/s against an 8 KB/s floor) must not be aborted,
// and the mirror must never be touched.
func TestApplyKeepsSourceAboveRateFloor(t *testing.T) {
	// Incompressible payload: a repetitive one would gzip down to a few
	// hundred bytes and complete before the rate window ever closes.
	payload := make([]byte, 16*1024)
	rand.New(rand.NewSource(42)).Read(payload)
	archive := makeTarGz(t, "golder", payload)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == checksumsFile {
			_, _ = w.Write([]byte(sums))
			return
		}
		// Announce the length so the watchdog's completion guard is exercised:
		// the final window holds only the tail of a finished transfer.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
		w.WriteHeader(http.StatusOK)
		// 2 KB every 100ms ≈ 20 KB/s, slow but well above the 8 KB/s floor.
		for off := 0; off < len(archive); off += 2048 {
			end := off + 2048
			if end > len(archive) {
				end = len(archive)
			}
			if _, err := w.Write(archive[off:end]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}))
	defer direct.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("mirror must not be used when the source holds the floor")
	}))
	defer mirror.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:         direct.Client(),
		ReleaseBaseURL:     direct.URL,
		Mirrors:            []string{mirror.URL + "/"},
		GOOS:               "linux",
		GOARCH:             "amd64",
		ExecPath:           target,
		MinRateBytesPerSec: 8 * 1024,
		RateWindow:         150 * time.Millisecond,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, payload) {
		t.Errorf("target length = %d, want %d", len(got), len(payload))
	}
	if strings.Contains(out.String(), "trying") {
		t.Errorf("source above the floor triggered a fallback:\n%s", out.String())
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

// ladderMirror is one mirror's answer in the checksums trust-ladder tests:
// an empty sums body or nil archive means "this path 404s".
type ladderMirror struct {
	sums    string
	archive []byte
}

// startLadderServers returns a direct base URL that fails everything and one
// httptest mirror per entry, so the checksums ladder is the only way through.
func startLadderServers(t *testing.T, mirrors []ladderMirror) (string, []string) {
	t.Helper()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unreachable", http.StatusBadGateway)
	}))
	t.Cleanup(direct.Close)
	urls := make([]string, 0, len(mirrors))
	for _, m := range mirrors {
		m := m
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if filepath.Base(r.URL.Path) == checksumsFile {
				if m.sums == "" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(m.sums))
				return
			}
			if m.archive == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(m.archive)
		}))
		t.Cleanup(srv.Close)
		urls = append(urls, srv.URL+"/")
	}
	return direct.URL, urls
}

// TestChecksumsMirrorAgreement: GitHub direct is unreachable for checksums.txt;
// two independent mirrors serve the same digest for the archive, so the update
// proceeds and the corroboration is reported.
func TestChecksumsMirrorAgreement(t *testing.T) {
	binary := []byte("agreed binary")
	archive := makeTarGz(t, "golder", binary)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct, mirrors := startLadderServers(t, []ladderMirror{
		{sums: sums, archive: archive},
		{sums: sums, archive: archive},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:     http.DefaultClient,
		ReleaseBaseURL: direct,
		Mirrors:        mirrors,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, binary) {
		t.Errorf("target = %q, want %q", got, binary)
	}
	if !strings.Contains(out.String(), "2 independent sources agree") {
		t.Errorf("expected a corroboration notice, got:\n%s", out.String())
	}
}

// TestChecksumsMirrorDisagreementAborts: one mirror serves the real digest and
// another a forged one, so the update must abort as possible tampering and
// leave the target untouched.
func TestChecksumsMirrorDisagreementAborts(t *testing.T) {
	archive := makeTarGz(t, "golder", []byte("real binary"))
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	realSums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)
	forged := makeTarGz(t, "golder", []byte("forged binary"))
	forgedSum := sha256.Sum256(forged)
	forgedSums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(forgedSum[:]), archiveName)

	direct, mirrors := startLadderServers(t, []ladderMirror{
		{sums: realSums, archive: archive},
		{sums: forgedSums, archive: forged},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:     http.DefaultClient,
		ReleaseBaseURL: direct,
		Mirrors:        mirrors,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	err := u.Apply(context.Background(), "v0.4.0", &out)
	if err == nil {
		t.Fatalf("Apply succeeded despite mirror disagreement\noutput:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "disagree") {
		t.Errorf("error = %v, want a disagreement message", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("target modified despite disagreement: %q", got)
	}
}

// TestChecksumsSingleMirrorWarns: a blocked network where only one mirror
// answers still installs, but the missing cross-check is called out.
func TestChecksumsSingleMirrorWarns(t *testing.T) {
	binary := []byte("lone mirror binary")
	archive := makeTarGz(t, "golder", binary)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct, mirrors := startLadderServers(t, []ladderMirror{
		{sums: sums, archive: archive},
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:     http.DefaultClient,
		ReleaseBaseURL: direct,
		Mirrors:        mirrors,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, binary) {
		t.Errorf("target = %q, want %q", got, binary)
	}
	if !strings.Contains(out.String(), "warning: checksums.txt served only by") {
		t.Errorf("expected a lone-source warning, got:\n%s", out.String())
	}
}

// TestChecksumsDirectIsPreferred: when GitHub direct serves checksums.txt and
// the archive, mirrors are never consulted.
func TestChecksumsDirectIsPreferred(t *testing.T) {
	binary := []byte("direct binary")
	archive := makeTarGz(t, "golder", binary)
	sum := sha256.Sum256(archive)
	archiveName := "golder_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == checksumsFile {
			_, _ = w.Write([]byte(sums))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer direct.Close()
	mirrorHits := 0
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits++
		http.Error(w, "must not be used", http.StatusInternalServerError)
	}))
	defer mirror.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "golder")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	var out bytes.Buffer
	u := &Updater{
		HTTPClient:     http.DefaultClient,
		ReleaseBaseURL: direct.URL,
		Mirrors:        []string{mirror.URL + "/"},
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &out); err != nil {
		t.Fatalf("Apply: %v\noutput:\n%s", err, out.String())
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, binary) {
		t.Errorf("target = %q, want %q", got, binary)
	}
	if mirrorHits != 0 {
		t.Errorf("mirror consulted %d times despite a healthy direct source", mirrorHits)
	}
	if strings.Contains(out.String(), "agree") || strings.Contains(out.String(), "warning") {
		t.Errorf("direct source should not produce ladder notices, got:\n%s", out.String())
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
