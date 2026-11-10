package mygoui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/config"
)

func TestGUIUpdateCheckAndDownloadUseSavedProxy(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	requests := make(chan string, 2)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.Host
		// Refuse the tunnel: no request should ever reach real GitHub in this test.
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: proxy.URL, Browser: false, Providers: []string{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := newGUIReleaseClient()
	defer client.CloseIdleConnections()
	_, err := fetchGUIRelease(ctx, client)
	if err == nil || !strings.Contains(err.Error(), "configured download proxy") {
		t.Fatalf("update check did not use the saved proxy: %v", err)
	}
	pkg := guiTestPackage(t, []byte("package"), "darwin", "arm64")
	client = newGUIDownloadClient()
	defer client.CloseIdleConnections()
	_, err = downloadGUIPackage(ctx, client, pkg, t.TempDir(), func(string, string) error {
		t.Error("a failed proxy request reached verification/publication")
		return nil
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "configured download proxy") {
		t.Fatalf("GUI package download did not use the saved proxy: %v", err)
	}
	for _, want := range []string{"CONNECT api.github.com:443", "CONNECT github.com:443"} {
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("proxy route=%q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("the configured proxy never received %s", want)
		}
	}
}
