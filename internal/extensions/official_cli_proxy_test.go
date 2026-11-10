package extensions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/config"
)

func TestOfficialCLIInstallBothUseConfiguredProxy(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	requests := make(chan string, 2)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: proxy.URL, Browser: false, Providers: []string{}}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []OfficialCLIKind{OfficialLarkCLI, OfficialProjectCLI} {
		i := NewOfficialCLIInstaller()
		i.Root = t.TempDir()
		i.Client.Timeout = 5 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := i.Install(ctx, kind)
		cancel()
		i.Client.CloseIdleConnections()
		if err == nil || !strings.Contains(err.Error(), "configured download proxy") {
			t.Fatalf("%s did not use the configured proxy: %v", kind, err)
		}
		want := "CONNECT github.com:443"
		if kind == OfficialProjectCLI {
			want = "CONNECT registry.npmjs.org:443"
		}
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("%s: proxy route=%q, want %q", kind, got, want)
			}
		default:
			t.Fatalf("%s never reached the configured proxy", kind)
		}
	}
}
