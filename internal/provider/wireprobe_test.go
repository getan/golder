package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeAccepted pins the body check that separates a real wire answer from
// a gateway's web UI. A 2xx + HTML is the shape that made a root-level
// /messages look like a working Anthropic endpoint; a 2xx + JSON error is the
// other false positive.
func TestProbeAccepted(t *testing.T) {
	cases := []struct {
		name        string
		wire        Wire
		contentType string
		body        string
		want        bool
	}{
		{"responses", WireResponses, "application/json", `{"object":"response","id":"resp_1","output":[]}`, true},
		{"responses wrong shape", WireResponses, "application/json", `{"choices":[]}`, false},
		{"messages", WireMessages, "application/json", `{"type":"message","content":[]}`, true},
		{"messages HTML", WireMessages, "text/html; charset=utf-8", `<!doctype html><html></html>`, false},
		{"messages HTML without content type", WireMessages, "", `<!doctype html><html></html>`, false},
		{"messages error with 200", WireMessages, "application/json", `{"type":"error","error":{"message":"no"}}`, false},
		{"chat", WireChat, "application/json", `{"choices":[]}`, true},
		{"gemini", WireGemini, "application/json", `{"candidates":[]}`, true},
		{"empty", WireResponses, "application/json", "", false},
	}
	for _, c := range cases {
		if got := probeAccepted(c.wire, c.contentType, []byte(c.body)); got != c.want {
			t.Errorf("%s: probeAccepted(%s, %q, %q) = %v, want %v", c.name, c.wire, c.contentType, c.body, got, c.want)
		}
	}
}

// TestProbeWireRejectsHTML200AndUsesCandidates reproduces the reported
// misdetection: /responses answers 200 + HTML, /chat/completions answers a real
// chat body, and /messages would answer 200 + HTML too. The probe must skip the
// HTML, accept Chat, and never ask a wire outside the candidate list.
func TestProbeWireRejectsHTML200AndUsesCandidates(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chat_1","choices":[{"message":{"content":"pong"}}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>Sub2API</body></html>`)
	}))
	defer srv.Close()

	got, err := ProbeWire(context.Background(), srv.URL, "", "gpt-6.1-sol", []Wire{WireResponses, WireChat}, 5*time.Second)
	if err != nil {
		t.Fatalf("ProbeWire: %v", err)
	}
	if got != WireChat {
		t.Fatalf("ProbeWire = %q, want chat after the HTML responses probe", got)
	}
	if len(paths) != 2 || paths[0] != "/responses" || paths[1] != "/chat/completions" {
		t.Errorf("probed paths = %v, want exactly /responses then /chat/completions", paths)
	}
}

// TestCachedWireOutsideCapabilitiesIsIgnored reproduces the cached half of the
// report: an old probe stored anthropic for openai + gpt, so EffectiveProtocol
// showed "anthropic" and the run used Messages even though openai declares only
// Chat and Responses. The cached answer must not override the provider's
// declared wires.
func TestCachedWireOutsideCapabilitiesIsIgnored(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	storeWire("openai", "gpt-6.1-sol", WireMessages)

	spec, ok := LookupProviderSpec("openai")
	if !ok {
		t.Fatal("openai spec missing")
	}
	if wireAllowed(spec, WireMessages) {
		t.Fatal("openai must not declare the Messages wire")
	}
	if got := EffectiveProtocol("openai", "gpt-6.1-sol", ""); got != ProtocolOpenAIResponses {
		t.Errorf("EffectiveProtocol = %q, want %q; the cached Messages wire is outside openai's capabilities", got, ProtocolOpenAIResponses)
	}
	off := func(key string) string {
		if key == "GOLDER_WIRE_PROBE" {
			return "off"
		}
		return ""
	}
	if got := selectWireWithProbe(spec, "gpt-6.1-sol", "", off); got != WireResponses {
		t.Errorf("selectWireWithProbe = %q, want responses", got)
	}

	// A declared wire is still honored: opencode-go declares Messages, so a
	// cached Messages answer for a Claude model stays authoritative.
	goSpec, ok := LookupProviderSpec("opencode-go")
	if !ok {
		t.Fatal("opencode-go spec missing")
	}
	storeWire("opencode-go", "claude-haiku-5-5", WireMessages)
	if got := selectWireWithProbe(goSpec, "claude-haiku-5-5", "", off); got != WireMessages {
		t.Errorf("opencode-go cached Messages = %q, want messages", got)
	}
}
