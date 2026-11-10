package downloadnet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
)

func TestDownloadProxyUsesConfiguredAddressRegardlessOfOtherRoutes(t *testing.T) {
	for _, raw := range []string{"http://proxy.test:7897", "https://proxy.test:7897", "socks5://proxy.test:7897", "socks5h://proxy.test:7897"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("GOLDER_HOME", t.TempDir())
			t.Setenv("GOLDER_PROXY", "")
			if err := config.SaveProxyConfig(config.ProxyConfig{URL: raw, Browser: false, Providers: []string{}}); err != nil {
				t.Fatal(err)
			}
			route, ok := Transport().(*proxyTransport)
			if !ok {
				t.Fatal("configured address was ignored because browser/provider routing was disabled")
			}
			defer route.CloseIdleConnections()
			req, _ := http.NewRequest(http.MethodGet, "https://github.com/getan/golder", nil)
			u, err := route.base.Proxy(req)
			if err != nil || u.String() != raw {
				t.Fatalf("wrong download proxy: %v %v", u, err)
			}
		})
	}
}

func TestDownloadProxyEnvironmentOverrideAndFreshSettings(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "http://override.test:9000")
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: "http://saved.test:7897"}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/file", nil)
	for _, want := range []string{"http://override.test:9000", "http://saved.test:7897"} {
		tr := Transport().(*proxyTransport)
		u, err := tr.base.Proxy(req)
		tr.CloseIdleConnections()
		if err != nil || u.String() != want {
			t.Fatalf("proxy route was stale or override was ignored: %v %v", u, err)
		}
		t.Setenv("GOLDER_PROXY", "")
	}
}

func TestDownloadProxyErrorsDoNotFallBackOrLeakCredentials(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "http://operator:private-password@proxy.test:7897")
	tr := Transport().(*proxyTransport)
	defer tr.CloseIdleConnections()
	calls := 0
	tr.base.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		calls++
		if address != "proxy.test:7897" {
			t.Errorf("request silently fell back to a direct connection: %s", address)
		}
		return nil, context.DeadlineExceeded
	}
	client := &http.Client{Transport: tr}
	_, err := client.Get("https://github.com/getan/golder/releases/download/v1.2.3/file")
	if err == nil || calls != 1 || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "configured download proxy") || strings.Contains(err.Error(), "private-password") {
		t.Fatalf("unsafe or unhelpful proxy failure: calls=%d err=%v", calls, err)
	}
	// Do not expose credentials even if an underlying proxy implementation puts
	// them in its error. Classification still sees the original cause.
	err = proxyError{cause: errors.New("http://operator:private-password@proxy.test failed")}
	if strings.Contains(err.Error(), "private-password") {
		t.Fatal("proxy transport exposed credentials")
	}
}

func TestDownloadProxyMissingInvalidAndUnreadableSettings(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	if Transport() != http.DefaultTransport {
		t.Fatal("unconfigured downloads must retain their existing network behavior")
	}
	for _, raw := range []string{"ftp://private-password@proxy.test", "http://proxy.test?password=private-password"} {
		t.Setenv("GOLDER_PROXY", raw)
		req, _ := http.NewRequest(http.MethodGet, "https://github.com/file", nil)
		_, err := Transport().RoundTrip(req)
		if err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("invalid configuration was ignored or leaked: %v", err)
		}
	}
	t.Setenv("GOLDER_PROXY", "")
	if err := os.WriteFile(config.ProxyConfigPath(), []byte(`url = "private-password`), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/file", nil)
	_, err := Transport().RoundTrip(req)
	if err == nil || strings.Contains(err.Error(), "private-password") {
		t.Fatalf("malformed proxy file fell back to direct or leaked config text: %v", err)
	}
}
