package provider

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

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
	return tr
}()

type proxyConfigError struct{ err error }

func (e proxyConfigError) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

// Every provider request gets an explicit routing choice. In particular, an
// unchecked provider never inherits HTTP_PROXY/HTTPS_PROXY from the shell.
func clientForURL(name, rawURL string) *http.Client {
	cfg, err := ProxySettings()
	if err != nil {
		return &http.Client{Transport: proxyConfigError{err}}
	}
	if !proxySelected(cfg, name, rawURL) {
		return &http.Client{Transport: directTransport}
	}
	raw := effectiveProxyURL(cfg)
	if raw == "" {
		return &http.Client{Transport: proxyConfigError{fmt.Errorf("proxy selected for %s but no address configured; use /proxy url <url> or /proxy %s off", name, name)}}
	}
	if err := ValidateProxyURL(raw); err != nil {
		return &http.Client{Transport: proxyConfigError{err}}
	}
	u, _ := url.Parse(raw)
	tr := directTransport.Clone()
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: tr}
}
