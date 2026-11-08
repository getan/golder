package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func fixture(data []byte) nativeFile {
	sum := sha256.Sum256(data)
	return nativeFile{
		Name:   "libghostty-vt.dylib",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-pinned/libghostty-vt-darwin-arm64.dylib",
		SHA256: hex.EncodeToString(sum[:]),
	}
}

func TestRunUsesManifestAndMyGoCache(t *testing.T) {
	home := t.TempDir()
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", home)
	case "windows":
		t.Setenv("LocalAppData", home)
	default:
		t.Setenv("XDG_CACHE_HOME", home)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("already verified")
	f := fixture(data)
	var m manifest
	m.Libraries = append(m.Libraries, struct {
		Name  string                `json:"name"`
		Files map[string]nativeFile `json:"files"`
	}{"libghostty-vt", map[string]nativeFile{"darwin-arm64": f, "linux-arm64": f}})
	manifestData, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(home, "mygo-plugin.json")
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(cache, "mygo", "natives", f.SHA256, f.Name)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(manifestPath, "darwin/arm64,linux/arm64"); err != nil {
		t.Fatal(err)
	}
	if err := run(manifestPath, "windows/arm64"); err == nil {
		t.Fatal("accepted a missing platform")
	}
}

func TestFetchVerifiesCachesAndRepairs(t *testing.T) {
	data := []byte("verified library")
	f := fixture(data)
	cache := t.TempDir()
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		var body string
		switch req.URL.Path {
		case "/repos/egoist/mygo/releases/tags/libghostty-vt-pinned":
			if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("metadata Accept = %q", got)
			}
			body = `{"assets":[{"id":42,"name":"libghostty-vt-darwin-arm64.dylib"}]}`
		case "/repos/egoist/mygo/releases/assets/42":
			if got := req.Header.Get("Accept"); got != "application/octet-stream" {
				t.Errorf("asset Accept = %q", got)
			}
			body = string(data)
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	call := func() {
		t.Helper()
		if err := fetch(context.Background(), client, apiRoot, cache, "test-token", f); err != nil {
			t.Fatal(err)
		}
	}
	call()
	dest := filepath.Join(cache, "mygo", "natives", f.SHA256, f.Name)
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(data) {
		t.Fatalf("cached library = %q, err=%v", got, err)
	}
	call()
	if requests != 2 {
		t.Fatalf("verified cache made extra requests: %d", requests)
	}
	if err := os.WriteFile(dest, []byte("corrupted"), 0644); err != nil {
		t.Fatal(err)
	}
	call()
	if requests != 4 {
		t.Fatalf("corrupted cache was not replaced: %d requests", requests)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(data) {
		t.Fatalf("repaired library = %q, err=%v", got, err)
	}
}

func TestFetchRejectsUnverifiedAsset(t *testing.T) {
	f := fixture([]byte("expected"))
	cache := t.TempDir()
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"assets":[{"id":42,"name":"libghostty-vt-darwin-arm64.dylib"}]}`
		if strings.Contains(req.URL.Path, "/assets/") {
			body = "wrong content"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	err := fetch(context.Background(), client, apiRoot, cache, "", f)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "mygo", "natives", f.SHA256, f.Name)); !os.IsNotExist(err) {
		t.Fatalf("unverified library entered the cache: %v", err)
	}
}

func TestFetchRejectsMissingOrAmbiguousAssets(t *testing.T) {
	f := fixture([]byte("expected"))
	for _, body := range []string{
		`{"assets":[]}`,
		`{"assets":[{"id":0,"name":"libghostty-vt-darwin-arm64.dylib"}]}`,
		`{"assets":[{"id":1,"name":"libghostty-vt-darwin-arm64.dylib"},{"id":2,"name":"libghostty-vt-darwin-arm64.dylib"}]}`,
	} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		if err := fetch(context.Background(), client, apiRoot, t.TempDir(), "", f); err == nil {
			t.Errorf("accepted release metadata %s", body)
		}
	}
}

func TestValidateRejectsUnsafeManifest(t *testing.T) {
	valid := fixture([]byte("data"))
	if _, _, err := validate(valid); err != nil {
		t.Fatal(err)
	}
	for _, f := range []nativeFile{
		{Name: "../evil", URL: valid.URL, SHA256: valid.SHA256},
		{Name: `..\evil`, URL: valid.URL, SHA256: valid.SHA256},
		{Name: valid.Name, URL: valid.URL, SHA256: strings.ToUpper(valid.SHA256)},
		{Name: valid.Name, URL: "https://attacker.test/egoist/mygo/releases/download/v1/file", SHA256: valid.SHA256},
		{Name: valid.Name, URL: "https://github.com/egoist/mygo/releases/download/../file", SHA256: valid.SHA256},
		{Name: valid.Name, URL: valid.URL + "?secret=1", SHA256: valid.SHA256},
	} {
		if _, _, err := validate(f); err == nil {
			t.Errorf("accepted unsafe manifest: %+v", f)
		}
	}
}

func TestSafeRedirectStripsCredentialsAndRejectsOtherHosts(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/asset", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	if err := safeRedirect(request, []*http.Request{{}}); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("credentials forwarded to the asset host")
	}
	request.URL.Host = "attacker.test"
	if err := safeRedirect(request, []*http.Request{{}}); err == nil {
		t.Fatal("accepted untrusted redirect")
	}
}

// Opt-in smoke test: download all release targets through the actual GitHub
// API into a temporary cache. No local MyGo cache or repository file is changed.
func TestLiveMyGoReleaseAssets(t *testing.T) {
	manifestPath := os.Getenv("GOLDER_TEST_NATIVE_MANIFEST")
	if manifestPath == "" {
		t.Skip("set GOLDER_TEST_NATIVE_MANIFEST for the network smoke test")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: safeRedirect}
	cache := t.TempDir()
	var count int
	for _, lib := range m.Libraries {
		if lib.Name != "libghostty-vt" {
			continue
		}
		for _, target := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64"} {
			f, ok := lib.Files[target]
			if !ok {
				t.Fatalf("missing target %s", target)
			}
			if err := fetch(context.Background(), client, apiRoot, cache, os.Getenv("GH_TOKEN"), f); err != nil {
				t.Fatalf("%s: %v", target, err)
			}
			count++
		}
	}
	if count != 5 {
		t.Fatalf("tested %d native libraries, want 5", count)
	}
}
