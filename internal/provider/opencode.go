package provider

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// OpencodeSessionHeader carries the client session id to opencode's Zen
// endpoints. The Go endpoint (modelList "lite") rejects requests without it
// (400 MissingSessionID: "cannot be routed efficiently"), using the value for
// sticky upstream routing. The full Zen endpoint tolerates its absence.
const OpencodeSessionHeader = "x-opencode-session"

// ExtraSessionID is the StreamConfig.Extra key carrying the golder session id
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
	return requestSessionID(extra)
}

// requestSessionID returns the session id carried in a StreamConfig.Extra map,
// trimmed, or "" when absent or blank. It is provider-agnostic: opencode
// providers also send the value as the sticky-routing header, and the
// Responses driver uses it as the backend's prompt_cache_key.
func requestSessionID(extra map[string]any) string {
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

// ProcessSessionID returns a stable per-process session identity for callers
// that need the opencode sticky-routing header but have no real session id
// (e.g. the risk reviewer, or a process-isolated sub-agent). Sharing one
// affinity bucket with the process's runs is the desired routing behavior
// there anyway.
var ProcessSessionID = sync.OnceValue(func() string {
	return fmt.Sprintf("golder-%d-%d", os.Getpid(), time.Now().Unix())
})
