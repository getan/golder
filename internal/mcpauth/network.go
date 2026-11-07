package mcpauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func authorizationSchemeAllowed(u *url.URL, resource string) bool {
	if u.Scheme == "https" {
		return true
	}
	base, err := url.Parse(resource)
	return err == nil && u.Scheme == "http" && base.Scheme == "http" && sameAddress(u.Host, base.Host) && isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameAddress(a, b string) bool { return strings.EqualFold(a, b) }

// safeClient prevents OAuth metadata from directing the host into unrelated
// loopback/private networks. The already-reviewed resource itself is allowed;
// other endpoints must resolve to public IPs. Redirects and ambient proxies are
// disabled so neither can bypass those checks or receive credentials.
func safeClient(resource string) (*http.Client, error) {
	base, err := url.Parse(resource)
	if err != nil || base.User != nil || base.Host == "" || (base.Scheme != "https" && !(base.Scheme == "http" && isLoopbackHost(base.Hostname()))) {
		return nil, errors.New("invalid MCP OAuth resource")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, errors.New("invalid OAuth network address")
		}
		approvedHost := strings.EqualFold(host, base.Hostname())
		approvedPort := base.Port()
		if approvedPort == "" {
			if base.Scheme == "https" {
				approvedPort = "443"
			} else {
				approvedPort = "80"
			}
		}
		// A named public resource may later resolve to a private IP. Only an
		// explicitly reviewed IP literal or localhost may reach private space.
		approved := approvedHost && port == approvedPort && (isLoopbackHost(base.Hostname()) || net.ParseIP(base.Hostname()) != nil)
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("OAuth endpoint DNS lookup failed")
		}
		var dialer net.Dialer
		for _, resolved := range ips {
			ip := resolved.IP
			if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || isSharedAddress(ip) {
				continue
			}
			if !approved && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback()) {
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("OAuth endpoint is unreachable or blocked by network policy")
	}
	return &http.Client{
		Transport: &oauthTransport{base: transport, resource: resource},
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("OAuth network redirects are disabled")
		},
	}, nil
}

type oauthTransport struct {
	base     http.RoundTripper
	resource string
}

func (t *oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.User != nil || !authorizationSchemeAllowed(r.URL, t.resource) {
		return nil, errors.New("unsafe OAuth network endpoint")
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.Body != nil {
		resp.Body = &limitedOAuthBody{ReadCloser: resp.Body, limit: io.LimitReader(resp.Body, 1<<20)}
	}
	return resp, err
}

type limitedOAuthBody struct {
	io.ReadCloser
	limit io.Reader
}

func (b *limitedOAuthBody) Read(p []byte) (int, error) { return b.limit.Read(p) }

func isSharedAddress(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && (v4[0] == 100 && v4[1]&0xc0 == 64 || v4[0] == 198 && (v4[1] == 18 || v4[1] == 19))
}
