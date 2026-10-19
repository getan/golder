package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

	got, err := ProbeWire(context.Background(), nil, srv.URL, "", "gpt-6.1-sol", []Wire{WireResponses, WireChat}, 5*time.Second)
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

	// The family table answers Claude on Messages and opencode-go serves that
	// wire (declared variant), so the run speaks Messages whether or not anything
	// is cached.
	goSpec, ok := LookupProviderSpec("opencode-go")
	if !ok {
		t.Fatal("opencode-go spec missing")
	}
	storeWire("opencode-go", "claude-haiku-5-5", WireMessages)
	if got := selectWireWithProbe(goSpec, "claude-haiku-5-5", "", off); got != WireMessages {
		t.Errorf("opencode-go cached Messages = %q, want messages", got)
	}
}

// probeOnEnv is the env a test injects when it wants the lazy probe to run it:
// TestMain turns the probe off process-wide, and every path here reads the
// injected getter rather than the process environment.
func probeOnEnv(string) string { return "" }

// TestProbeWireDoesNotSettleWhenABetterWireIsSlow reproduces the reported
// misdetection from the other side. /responses is a working endpoint that answers
// slower than the probe's budget (the report measured 12-19s for a 24KB body
// through a relay; here the request is simply held), while /chat/completions
// answers immediately. A wire that never answered is not a wire that refused, so
// the probe must report that it could not settle the question instead of handing
// the model to Chat — and it must not even ask Chat, whose answer could not
// settle it either.
func TestProbeWireDoesNotSettleWhenABetterWireIsSlow(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/responses" {
			<-release
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat_1","choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer srv.Close()
	defer close(release)

	got, err := ProbeWire(context.Background(), nil, srv.URL, "", "gpt-6.1-sol", []Wire{WireResponses, WireChat}, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("ProbeWire = %q with no error, want the probe to report that it could not settle", got)
	}
	if !strings.Contains(err.Error(), "did not answer within") {
		t.Errorf("error = %v, want it to name the timeout", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/responses" {
		t.Errorf("probed paths = %v, want only /responses: Chat's answer could not settle the question", paths)
	}
}

// countingRoundTripper counts the requests a probe sends through the route it was
// handed, standing in for the provider's own client (HTTPClientFor).
type countingRoundTripper struct {
	base http.RoundTripper
	n    int32
}

func (c *countingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	atomic.AddInt32(&c.n, 1)
	return c.base.RoundTrip(r)
}

// TestProbeUsesTheRunsRouteAndLeavesItIntact pins the routing half of the fix:
// the probe asks over the client the run's own requests use, so the answer
// describes the endpoint that will actually be reached, and it copies that client
// instead of mutating a route's shared one.
func TestProbeUsesTheRunsRouteAndLeavesItIntact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chat_1","choices":[{"message":{"content":"pong"}}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	counter := &countingRoundTripper{base: http.DefaultTransport}
	client := &http.Client{Transport: counter}
	got, err := ProbeWire(context.Background(), client, srv.URL, "", "gpt-6.1-sol", []Wire{WireResponses, WireChat}, 5*time.Second)
	if err != nil {
		t.Fatalf("ProbeWire: %v", err)
	}
	if got != WireChat {
		t.Fatalf("ProbeWire = %q, want chat", got)
	}
	if n := atomic.LoadInt32(&counter.n); n != 2 {
		t.Errorf("round trips on the caller's client = %d, want 2: the probe must ask over the route the run uses", n)
	}
	if client.Timeout != 0 {
		t.Errorf("the shared client's timeout = %s, want it untouched: a route's client is shared", client.Timeout)
	}
}

// TestFamilyRuleDecidesWithoutProbing pins the answer to the report: the family
// table (ModelWire) is a public rule, not a guess about an endpoint, so a gateway
// that serves a family's own wire is never asked about it. The probe that once
// recorded openai/chat for a slow endpoint has no question left to ask.
func TestFamilyRuleDecidesWithoutProbing(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	spec, ok := LookupProviderSpec("openai")
	if !ok {
		t.Fatal("openai spec missing")
	}
	var asked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[]}`)
	}))
	defer srv.Close()

	for _, model := range []string{
		"gpt-6.1-sol", "gpt-5.3-codex", "codex-mini", "o3-mini",
		"grok-4.7", "muse-spark-1.3-contributor", "deepseek-v4.1-flash",
	} {
		if got := selectWireWithProbe(spec, model, srv.URL, probeOnEnv); got != WireResponses {
			t.Errorf("selectWireWithProbe(openai, %q) = %q, want responses (the family table's own wire)", model, got)
		}
	}
	if _, busy := probeInflight.Load(wireCacheKey(spec.Name, "gpt-6.1-sol")); busy {
		t.Error("a probe is in flight for a family the table answers")
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&asked); n != 0 {
		t.Errorf("%d probe request(s) reached the endpoint, want 0: a family rule is not a question", n)
	}
}

// TestMessagesFamilyIsNotProbed is the same for the other wire: the table puts
// Claude on Messages, opencode-go and commandcode serve that wire, so neither
// asks, and no cache entry is needed to keep them there.
func TestMessagesFamilyIsNotProbed(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	var asked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	for _, name := range []string{"opencode-go", "commandcode"} {
		spec, ok := LookupProviderSpec(name)
		if !ok {
			t.Fatalf("provider %q missing from the registry", name)
		}
		// A routed id ("anthropic/claude-…", as commandcode lists it) is the same
		// family to the table.
		for _, model := range []string{"claude-haiku-5-5", "anthropic/claude-opus-4-8"} {
			if got := selectWireWithProbe(spec, model, srv.URL, probeOnEnv); got != WireMessages {
				t.Errorf("selectWireWithProbe(%s, %q) = %q, want messages", name, model, got)
			}
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&asked); n != 0 {
		t.Errorf("%d probe request(s) reached the endpoint, want 0", n)
	}
}

// TestProbedAnswerCannotOverturnTheTableAndAServedOneCan is the reported case and
// its exception in one place. A probe once settled openai/gpt-6.1-sol on Chat and
// cached that for the model and the family; a greeting may not overturn the
// family table, so the run speaks Responses again. An answer a real request
// produced — the adaptive fallback watched the wire carry the model — is
// evidence, and does stand in: that is how a gateway which really diverges is
// recorded.
func TestProbedAnswerCannotOverturnTheTableAndAServedOneCan(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	storeWire("openai", "gpt-6.1-sol", WireChat)
	storeWire("openai", "family:gpt", WireChat)

	if got := EffectiveProtocol("openai", "gpt-6.1-sol", ""); got != ProtocolOpenAIResponses {
		t.Errorf("EffectiveProtocol = %q, want %q: a probe's answer is not this family's to change", got, ProtocolOpenAIResponses)
	}
	spec, ok := LookupProviderSpec("openai")
	if !ok {
		t.Fatal("openai spec missing")
	}
	if got := selectWireWithProbe(spec, "gpt-6.1-sol", "", probeOnEnv); got != WireResponses {
		t.Errorf("selectWireWithProbe = %q, want responses", got)
	}

	storeServedWire("openai", "gpt-6.1-sol", WireChat)
	if got := EffectiveProtocol("openai", "gpt-6.1-sol", ""); got != ProtocolOpenAI {
		t.Errorf("EffectiveProtocol = %q, want %q after a served answer", got, ProtocolOpenAI)
	}
	if got := selectWireWithProbe(spec, "gpt-6.1-sol", "", probeOnEnv); got != WireChat {
		t.Errorf("selectWireWithProbe = %q, want chat after a served answer", got)
	}
}

// TestEntitledProbeAnswerIsCachedForModelAndFamily keeps the other half honest:
// where the question is genuinely open — a gateway that does not serve the
// model's family wire, so the static pick is a translation — a settled probe
// answer is still stored (probe-grade) for the model and its family.
func TestEntitledProbeAnswerIsCachedForModelAndFamily(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chat_1","choices":[{"message":{"content":"pong"}}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// OpenRouter is chat-only: Claude's Messages wire is not served, so the static
	// answer is Chat and "does /responses carry it?" is a real question.
	spec, ok := LookupProviderSpec("openrouter")
	if !ok {
		t.Fatal("openrouter spec missing")
	}
	if got := selectWireWithProbe(spec, "claude-sonnet-4-5", srv.URL, probeOnEnv); got != WireChat {
		t.Errorf("the run keeps its static answer = %q, want chat", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, ok := CachedWire(spec.Name, "claude-sonnet-4-5"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the settled answer never reached the cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w, served, ok := CachedWire(spec.Name, "claude-sonnet-4-5"); !ok || w != WireChat || served {
		t.Errorf("cached wire = %q,served=%v,%v, want chat,false,true", w, served, ok)
	}
	if w, _, ok := CachedWire(spec.Name, "family:claude"); !ok || w != WireChat {
		t.Errorf("family cache = %q,%v, want chat,true", w, ok)
	}
}
