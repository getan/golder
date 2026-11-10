package remotecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SecurePort is the loopback-only backend for the operator-configured Serve
// proxy. It is deliberately fixed: changing it on conflict could route a
// different local process through an existing Serve configuration.
const SecurePort = 18787

// ServeIdentity is derived from the local Tailscale daemon, never from an HTTP
// header or a hostname supplied by a remote client.
type ServeIdentity struct {
	Origin    string
	NodeID    string
	TailnetID string
}

type tailscaleStatus struct {
	BackendState string
	CertDomains  []string
	Self         *struct {
		ID      string
		DNSName string
		Expired bool
	}
	CurrentTailnet *struct {
		Name            string
		StableID        string
		MagicDNSEnabled bool
	}
}

// These are the security-relevant fields of Tailscale's ipn.ServeConfig.
// Unknown Services/Foreground layouts are refused rather than guessed at.
type serveConfig struct {
	TCP map[string]struct {
		HTTPS         bool
		HTTP          bool
		TCPForward    string
		TerminateTLS  string
		ProxyProtocol int
	}
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy    string
			Path     string
			Text     string
			Redirect string
		}
	}
	AllowFunnel map[string]bool
	Foreground  map[string]json.RawMessage
	Services    map[string]json.RawMessage
}

// VerifyTailscaleServe reads, but never changes, the local Tailscale state.
// The caller must revalidate it while the service is active. No network probe
// is performed; this is not a substitute for a real device integration test.
func VerifyTailscaleServe(ctx context.Context) (ServeIdentity, error) {
	return verifyTailscaleServe(ctx, tailscaleJSON)
}

func verifyTailscaleServe(ctx context.Context, run func(context.Context, ...string) ([]byte, error)) (ServeIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := run(ctx, "status", "--json")
	if err != nil {
		return ServeIdentity{}, errors.New("cannot read Tailscale status; ensure Tailscale is installed and running")
	}
	serve, err := run(ctx, "serve", "status", "--json")
	if err != nil {
		return ServeIdentity{}, errors.New("cannot read Tailscale Serve configuration")
	}
	return parseServeIdentity(status, serve)
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("Tailscale response too large")
	}
	return b.Buffer.Write(p)
}

func tailscaleJSON(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "tailscale", args...)
	var out limitedOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func parseServeIdentity(statusJSON, serveJSON []byte) (ServeIdentity, error) {
	var status tailscaleStatus
	var cfg serveConfig
	decoder := json.NewDecoder(bytes.NewReader(serveJSON))
	decoder.DisallowUnknownFields()
	if json.Unmarshal(statusJSON, &status) != nil || decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ServeIdentity{}, errors.New("unrecognized Tailscale status or Serve configuration")
	}
	if status.BackendState != "Running" || status.Self == nil || status.Self.ID == "" || status.Self.Expired || status.CurrentTailnet == nil || status.CurrentTailnet.Name == "" || !status.CurrentTailnet.MagicDNSEnabled {
		return ServeIdentity{}, errors.New("Tailscale is not connected to a verified MagicDNS tailnet")
	}
	dns := strings.ToLower(strings.TrimSuffix(status.Self.DNSName, "."))
	cert := false
	for _, name := range status.CertDomains {
		if strings.EqualFold(strings.TrimSuffix(name, "."), dns) {
			cert = true
		}
	}
	if dns == "" || net.ParseIP(dns) != nil || !cert || strings.ContainsAny(dns, "/:@?# ") {
		return ServeIdentity{}, errors.New("Tailscale HTTPS certificate domain is unavailable")
	}
	if len(cfg.Foreground) != 0 || len(cfg.Services) != 0 {
		return ServeIdentity{}, errors.New("cannot safely verify foreground or Tailscale Service routes; configure a background node Serve route")
	}
	for _, enabled := range cfg.AllowFunnel {
		if enabled {
			return ServeIdentity{}, errors.New("Tailscale Funnel is enabled; disable it before remote control")
		}
	}
	var origin string
	for hostPort, web := range cfg.Web {
		host, port, err := net.SplitHostPort(hostPort)
		if err != nil {
			return ServeIdentity{}, errors.New("unrecognized Tailscale Serve host")
		}
		for mount, handler := range web.Handlers {
			proxy := handler.Proxy
			if proxy == strconv.Itoa(SecurePort) {
				return ServeIdentity{}, errors.New("ambiguous Tailscale Serve route for the remote-control port")
			}
			if proxy != "" && !strings.Contains(proxy, "://") {
				proxy = "http://" + proxy
			}
			u, err := url.Parse(proxy)
			if err != nil || u == nil || u.Port() != strconv.Itoa(SecurePort) {
				continue
			}
			if origin != "" || len(web.Handlers) != 1 || !strings.EqualFold(host, dns) || mount != "/" || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || handler.Path != "" || handler.Text != "" || handler.Redirect != "" {
				return ServeIdentity{}, errors.New("conflicting or unsafe Tailscale Serve route for the remote-control port")
			}
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return ServeIdentity{}, errors.New("invalid Tailscale HTTPS port")
			}
			tcp, ok := cfg.TCP[port]
			if !ok || !tcp.HTTPS || tcp.HTTP || tcp.TCPForward != "" || tcp.TerminateTLS != "" || tcp.ProxyProtocol != 0 {
				return ServeIdentity{}, errors.New("Tailscale Serve route is not HTTPS")
			}
			origin = "https://" + dns
			if p != 443 {
				origin = fmt.Sprintf("%s:%d", origin, p)
			}
		}
	}
	for _, tcp := range cfg.TCP {
		if _, port, err := net.SplitHostPort(tcp.TCPForward); err == nil && port == strconv.Itoa(SecurePort) {
			return ServeIdentity{}, errors.New("Tailscale TCP forwarding also targets the remote-control port")
		}
	}
	if origin == "" {
		return ServeIdentity{}, fmt.Errorf("configure a dedicated background Tailscale Serve HTTPS route to http://127.0.0.1:%d", SecurePort)
	}
	id := status.CurrentTailnet.StableID
	if id == "" {
		id = status.CurrentTailnet.Name
	}
	return ServeIdentity{Origin: origin, NodeID: status.Self.ID, TailnetID: id}, nil
}
