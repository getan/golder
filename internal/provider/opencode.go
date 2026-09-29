package provider

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// OpencodeSessionHeader carries the client session id to opencode's Zen
// endpoints. The Go endpoint (modelList "lite") rejects requests without it
// (400 MissingSessionID: "cannot be routed efficiently"), using the value for
// sticky upstream routing. The full Zen endpoint tolerates its absence.
const OpencodeSessionHeader = "x-opencode-session"

// ExtraSessionID is the StreamConfig.Extra key carrying the pigo session id
// (see RunConfig.SessionID) from the loop to the provider drivers. It is
// opaque to every other layer.
const ExtraSessionID = "session_id"

// SessionHeaderValue returns the x-opencode-session value for a request, or ""
// when no header should be sent. Only providers whose name starts with
// "opencode" ever send it (mirroring opencode's own client, which gates on
// model.providerID.startsWith("opencode")); every other provider ignores the
// session id entirely.
func SessionHeaderValue(providerName string, extra map[string]any) string {
	if !strings.HasPrefix(providerName, "opencode") {
		return ""
	}
	v, _ := extra[ExtraSessionID].(string)
	return strings.TrimSpace(v)
}

// WithSessionExtra returns a copy of extra carrying the session id under
// ExtraSessionID. It is a no-op (returns extra unchanged) when sid is empty
// or an id is already present, so an explicit value always wins and shared
// maps are never mutated.
func WithSessionExtra(extra map[string]any, sid string) map[string]any {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return extra
	}
	if v, _ := extra[ExtraSessionID].(string); strings.TrimSpace(v) != "" {
		return extra
	}
	out := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		out[k] = v
	}
	out[ExtraSessionID] = sid
	return out
}

// ProxyURL resolves the HTTP proxy for proxied upstreams from PIGO_PROXY
// only. Unset (or blank) means direct connection: other users work out of the
// box, and whoever needs egress (e.g. Muse behind opencode.ai requiring US
// exit) exports PIGO_PROXY=http://127.0.0.1:7897 in their own shell.
func ProxyURL() string {
	return strings.TrimSpace(os.Getenv("PIGO_PROXY"))
}

// NeedsProxy reports whether requests for the given provider / URL must go
// through the proxy. The registry's ForceProxy flag is authoritative (it keys
// on the provider name, so --base-url overrides keep the behavior); the
// opencode.ai hostname match is a fallback for callers that only know the URL
// (e.g. model discovery).
func NeedsProxy(providerName, rawURL string) bool {
	if spec, ok := LookupProviderSpec(providerName); ok && spec.ForceProxy {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(u.Hostname()), "opencode.ai")
}

// clientForURL returns an *http.Client routing via the proxy when NeedsProxy
// holds, else nil meaning "use the caller's default client".
func clientForURL(providerName, rawURL string) *http.Client {
	if !NeedsProxy(providerName, rawURL) {
		return nil
	}
	proxy := ProxyURL()
	if proxy == "" {
		return nil
	}
	pu, err := url.Parse(proxy)
	if err != nil {
		return nil
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
}
