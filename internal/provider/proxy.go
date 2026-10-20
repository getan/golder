package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/getan/golder/internal/cli/config"
)

// ProxySettings returns the user's provider selection, preserving the old
// OpenCode defaults until the first explicit selection is saved.
func ProxySettings() (config.ProxyConfig, error) {
	cfg, err := config.LoadProxyConfig()
	if err != nil {
		return cfg, err
	}
	if cfg.Providers == nil {
		cfg.Providers = []string{}
		if effectiveProxyURL(cfg) != "" {
			for _, spec := range ProviderSpecs() {
				if spec.ForceProxy {
					cfg.Providers = append(cfg.Providers, spec.Name)
				}
			}
		}
	}
	return cfg, nil
}

func effectiveProxyURL(cfg config.ProxyConfig) string {
	if v := strings.TrimSpace(os.Getenv("GOLDER_PROXY")); v != "" {
		return v
	}
	return strings.TrimSpace(cfg.URL)
}

// ProxyURL is an address only; setting it never enables every provider.
func ProxyURL() string {
	cfg, _ := ProxySettings()
	return effectiveProxyURL(cfg)
}

// HTTPClientFor returns the client a request to name/baseURL should use: the
// user's proxy when this provider is selected for proxying, a direct client
// otherwise. It exists so a caller outside this package — the decision-model
// reviewer — inherits the same routing rules as every provider request instead
// of building its own client and silently bypassing the user's proxy settings.
//
// The returned client is shared per route; treat it as read-only.
func HTTPClientFor(name, baseURL string) *http.Client {
	return clientForURL(name, baseURL)
}

func proxySelected(cfg config.ProxyConfig, name, rawURL string) bool {
	if spec, ok := LookupProviderSpec(name); ok {
		name = spec.Name
	}
	if name != "" {
		return slices.Contains(cfg.Providers, name)
	}
	// Compatibility for URL-only callers. Named callers always use the
	// explicit provider selection, including with a relay base URL.
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, spec := range ProviderSpecs() {
		base, err := url.Parse(spec.DefaultBaseURL)
		if err == nil && base.Hostname() != "" &&
			strings.EqualFold(u.Hostname(), base.Hostname()) &&
			slices.Contains(cfg.Providers, spec.Name) {
			return true
		}
	}
	return false
}

func NeedsProxy(name, rawURL string) bool {
	cfg, err := ProxySettings()
	return err == nil && proxySelected(cfg, name, rawURL)
}

func ValidateProxyURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
		return fmt.Errorf("proxy URL must use http://, https://, socks5:// or socks5h:// with a host")
	}
	return nil
}

// DisplayURL omits credentials, query strings and fragments from UI output.
func DisplayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

func ProxyStatus(name, baseURL string) string {
	cfg, err := ProxySettings()
	if err != nil {
		return "proxy: settings error"
	}
	if !proxySelected(cfg, name, baseURL) {
		return "direct"
	}
	if effectiveProxyURL(cfg) == "" {
		return "proxy selected; set /proxy url"
	}
	return "proxy"
}

var directTransport = func() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // Provider selection must not become a process-wide proxy.
	// The body guard only starts once response headers exist, so a server that
	// accepts the request and then never sends headers used to be unbounded.
	// Bound that phase with the same first-token budget; a timeout here is a
	// connect failure, which the stream retry already re-issues.
	tr.ResponseHeaderTimeout = firstContentTimeout(context.Background())
	return tr
}()

// maxCachedClients bounds the per-route client cache. Overflow drops the map
// (holders of an evicted client keep using it; the next request rebuilds its
// route), mirroring codex's bounded route-aware client pool.
const maxCachedClients = 16

// clientCache memoizes one *http.Client per resolved route — "direct" or a
// proxy URL — so consecutive requests share a transport and its connection
// pool. Without it every request through a proxy cloned a fresh transport and
// paid a new TCP+TLS handshake; streaming turns make that the common case
// rather than a cold start.
var (
	clientCacheMu sync.Mutex
	clientCache   = make(map[string]*http.Client)
)

// cachedClient returns the cached client for key, building and storing one on
// first use. Callers must not mutate the returned client: every request on the
// same route shares it (copy it first, as modelfetch does for its timeout).
func cachedClient(key string, build func() *http.Client) *http.Client {
	clientCacheMu.Lock()
	defer clientCacheMu.Unlock()
	if c, ok := clientCache[key]; ok {
		return c
	}
	if len(clientCache) >= maxCachedClients {
		clear(clientCache)
	}
	c := build()
	clientCache[key] = c
	return c
}

type proxyConfigError struct{ err error }

func (e proxyConfigError) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

// Every provider request gets an explicit routing choice. In particular, an
// unchecked provider never inherits HTTP_PROXY/HTTPS_PROXY from the shell.
// The client handed back is shared per route (see cachedClient); treat it as
// read-only.
func clientForURL(name, rawURL string) *http.Client {
	cfg, err := ProxySettings()
	if err != nil {
		return &http.Client{Transport: proxyConfigError{err}}
	}
	if !proxySelected(cfg, name, rawURL) {
		key := "direct"
		if streamTunedProvider(name) {
			key = "direct:" + strings.ToLower(strings.TrimSpace(name))
		}
		return cachedClient(key, func() *http.Client {
			return &http.Client{Transport: transportForProvider(name)}
		})
	}
	raw := effectiveProxyURL(cfg)
	if raw == "" {
		return &http.Client{Transport: proxyConfigError{fmt.Errorf("proxy selected for %s but no address configured; use /proxy url <url> or /proxy %s off", name, name)}}
	}
	if err := ValidateProxyURL(raw); err != nil {
		return &http.Client{Transport: proxyConfigError{err}}
	}
	key := "proxy:" + raw
	if streamTunedProvider(name) {
		key = "proxy:" + strings.ToLower(strings.TrimSpace(name)) + ":" + raw
	}
	return cachedClient(key, func() *http.Client {
		u, _ := url.Parse(raw)
		tr := transportForProvider(name)
		tr.Proxy = http.ProxyURL(u)
		return &http.Client{Transport: tr}
	})
}

// transportForProvider gives streaming gateways their own transport when their
// long-lived responses need it. Command Code's stream is the case: a compression
// buffer or a pooled keep-alive that the proxy has already closed can lose the
// tail of a long answer, so both are off. HTTP/2 stays on: forcing HTTP/1.x made
// the client parse the endpoint's h2 SETTINGS frame as a malformed HTTP response
// ("malformed HTTP response \x00\x00\x12\x04…"), because the endpoint serves h2.
//
// Every other provider keeps the shared direct transport's behavior.
func transportForProvider(name string) *http.Transport {
	tr := directTransport.Clone()
	if !streamTunedProvider(name) {
		return tr
	}
	tr.DisableCompression = true
	tr.DisableKeepAlives = true
	return tr
}

// streamTunedProvider reports whether a provider's stream gets the transport
// above. Keep the list short: each entry is a deliberate trade of connection
// reuse for a stream that survives the proxy.
func streamTunedProvider(name string) bool {
	if spec, ok := LookupProviderSpec(name); ok {
		name = spec.Name
	}
	return strings.EqualFold(strings.TrimSpace(name), "commandcode")
}
