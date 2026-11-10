// Package downloadnet applies Golder's configured proxy to operator-initiated
// package downloads, independently of provider and browser routing selections.
package downloadnet

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

// Transport snapshots the current download route. GOLDER_PROXY overrides the
// saved address; absent both, retain the existing default/environment transport.
// A configured but broken proxy never silently falls back to direct access.
func Transport() http.RoundTripper {
	raw := strings.TrimSpace(os.Getenv("GOLDER_PROXY"))
	if raw == "" {
		cfg, err := config.LoadProxyConfig()
		if err != nil {
			return configError{}
		}
		raw = strings.TrimSpace(cfg.URL)
	}
	if raw == "" {
		return http.DefaultTransport
	}
	if err := provider.ValidateProxyURL(raw); err != nil {
		return configError{}
	}
	u, _ := url.Parse(raw)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	return &proxyTransport{base: tr}
}

type configError struct{}

func (configError) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("download proxy settings are invalid or unavailable; check Settings > Proxy or GOLDER_PROXY")
}

type proxyTransport struct{ base *http.Transport }

func (t *proxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		// Some proxy implementations include credentials in transport errors.
		// Keep the cause for cancellation/timeout classification, not UI text.
		return resp, proxyError{cause: err}
	}
	return resp, nil
}

func (t *proxyTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

type proxyError struct{ cause error }

func (e proxyError) Error() string {
	return "request through the configured download proxy failed; check Settings > Proxy and proxy connectivity"
}

func (e proxyError) Unwrap() error { return e.cause }
