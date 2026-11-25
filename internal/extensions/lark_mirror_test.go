package extensions

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	larkversion "github.com/getan/golder/extension/plugins/lark/version"
)

type larkRoundTripper func(*http.Request) (*http.Response, error)

func (f larkRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestLarkReleaseFallsBackToMirrors(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_MIRROR", "https://m1.invalid/ https://m2.invalid/")
	release, err := larkRelease("1.5.140", "darwin", "arm64", "https://github.com/getan/golder/releases/download")
	if err != nil {
		t.Fatal(err)
	}
	archive := testLarkArchive(t, tar.TypeReg)
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
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		default:
			return nil, errors.New("direct GitHub blocked")
		}
	})}

	pkg, err := installLarkRelease(context.Background(), release, client)
	if err != nil {
		t.Fatalf("mirror fallback failed: %v", err)
	}
	if pkg.Version != larkversion.Version {
		t.Fatalf("installed version = %s, want %s", pkg.Version, larkversion.Version)
	}
}

func TestLarkChecksumMirrorsMustAgree(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_MIRROR", "https://m1.invalid/ https://m2.invalid/")
	release, err := larkRelease("1.5.140", "darwin", "arm64", "https://github.com/getan/golder/releases/download")
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
	if _, err := installLarkRelease(context.Background(), release, client); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("mirror disagreement was not rejected: %v", err)
	}
}
