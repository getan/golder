// Package setup defines the small operator-only setup event stream. It is not
// an MCP extension and intentionally has no token, executable or browser socket.
package setup

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

const Version = 1
const MaxLine = 16 << 10

type Event struct {
	Version         int       `json:"v"`
	Type            string    `json:"type"`
	Service         string    `json:"service,omitempty"`
	Message         string    `json:"message,omitempty"`
	VerificationURL string    `json:"verification_url,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	Authenticated   bool      `json:"authenticated,omitempty"`
}

// AuthorizationURL requires a reviewed exact HTTPS origin. URL text, including
// a verification code, is only for the operator UI, never a model tool result.
func AuthorizationURL(raw string, origins []string) error {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n") {
		return errors.New("invalid authorization URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("authorization URL must be HTTPS without userinfo or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("local authorization destinations are refused")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()) {
		return errors.New("local authorization destinations are refused")
	}
	for _, rawOrigin := range origins {
		o, e := url.Parse(rawOrigin)
		if e == nil && o.Scheme == u.Scheme && strings.EqualFold(o.Host, u.Host) {
			return nil
		}
	}
	return errors.New("authorization origin is not reviewed; add its exact HTTPS origin to setup_origins and reapprove the source")
}
