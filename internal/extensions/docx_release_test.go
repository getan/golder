package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	docxversion "github.com/getan/golder/extension/plugins/docx/version"
)

func testDocxArchive(t *testing.T, binary string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho docx-test\n")
	if err := tw.WriteHeader(&tar.Header{Name: binary, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDocxReleaseAssetName(t *testing.T) {
	release, err := docxRelease("1.5.160", "windows", "amd64", "https://github.com/getan/golder/releases/download")
	if err != nil {
		t.Fatal(err)
	}
	want := "golder-docx_1.5.160_helper" + docxversion.Version + "_windows_amd64.tar.gz"
	if release.Archive != want || release.Binary != "golder-docx.exe" {
		t.Fatalf("release = %+v, want archive %s and binary .exe", release, want)
	}
	if _, err := docxRelease("dev", "darwin", "arm64", "https://example.invalid/"); err == nil {
		t.Fatal("dev version was accepted")
	}
	if _, err := docxRelease("1.5.160", "plan9", "arm64", "https://example.invalid/"); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
}

func TestDocxReleaseFallsBackToMirrorsAndReportsProgress(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_MIRROR", "https://m1.invalid/ https://m2.invalid/")
	release, err := docxRelease("1.5.160", "darwin", "arm64", "https://github.com/getan/golder/releases/download")
	if err != nil {
		t.Fatal(err)
	}
	archive := testDocxArchive(t, release.Binary)
	digest := sha256.Sum256(archive)
	checksums := []byte(fmt.Sprintf("%x  %s\n", digest, release.Archive))
	client := &http.Client{Transport: larkRoundTripper(func(req *http.Request) (*http.Response, error) {
		raw := req.URL.String()
		switch {
		case strings.Contains(raw, "m1.invalid"), strings.Contains(raw, "m2.invalid"):
			body := archive
			if strings.HasSuffix(raw, "checksums.txt") {
				body = checksums
			}
			return &http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)),
				Header: make(http.Header), ContentLength: int64(len(body)),
			}, nil
		default:
			return nil, errors.New("direct GitHub blocked")
		}
	})}
	var stages []string
	var sawDownload, sawTotal bool
	pkg, err := installDocxReleaseWithProgress(context.Background(), release, client, func(p DocxDownloadProgress) {
		stages = append(stages, p.Stage)
		if p.Stage == "downloading" && p.Downloaded > 0 {
			sawDownload = true
		}
		if p.Total > 0 {
			sawTotal = true
		}
	})
	if err != nil {
		t.Fatalf("mirror fallback failed: %v", err)
	}
	if pkg.ID != "docx" || pkg.Version != docxversion.Version {
		t.Fatalf("installed package = %+v", pkg)
	}
	if !sawDownload || !sawTotal {
		t.Fatalf("missing byte progress: stages=%v download=%v total=%v", stages, sawDownload, sawTotal)
	}
	joined := strings.Join(stages, ",")
	for _, want := range []string{"checking", "downloading", "verifying", "extracting", "installing", "complete"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing stage %q in %s", want, joined)
		}
	}
	if !strings.Contains(pkg.Executable, "golder-docx") {
		t.Fatalf("unexpected executable %q", pkg.Executable)
	}
}

func TestDocxChecksumMirrorsMustAgree(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_MIRROR", "https://m1.invalid/ https://m2.invalid/")
	release, err := docxRelease("1.5.160", "darwin", "arm64", "https://github.com/getan/golder/releases/download")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: larkRoundTripper(func(req *http.Request) (*http.Response, error) {
		raw := req.URL.String()
		switch {
		case strings.Contains(raw, "m1.invalid"):
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", 64) + "  " + release.Archive + "\n")), Header: make(http.Header)}, nil
		case strings.Contains(raw, "m2.invalid"):
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("b", 64) + "  " + release.Archive + "\n")), Header: make(http.Header)}, nil
		default:
			return nil, errors.New("direct GitHub blocked")
		}
	})}
	if _, err := installDocxReleaseWithProgress(context.Background(), release, client, nil); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("mirror disagreement was not rejected: %v", err)
	}
}
