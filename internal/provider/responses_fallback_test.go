package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// TestModelProtocolUnsupportedFallsBackInPriorityOrder reproduces the reported
// opencode-go/domestic-model failure: Responses answers
// 400 ModelProtocolUnsupported, Messages answers the same, and Chat serves the
// request. The wrapper must try them in that order, surface Chat's answer, and
// record Chat in the wire cache for the model and family.
func TestModelProtocolUnsupportedFallsBackInPriorityOrder(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_WIRE_PROBE", "off")
	resetWireCache()

	const model = "glm-5.3"
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/responses", "/messages":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol."}`)
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"`+model+`","choices":[{"delta":{"content":"pong"}}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"`+model+`","choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, name, err := ResolveNamedProvider("opencode-go", model, srv.URL, "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveNamedProvider: %v", err)
	}
	if name != "opencode-go" {
		t.Fatalf("provider name = %q, want opencode-go", name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := p.StreamCompletion(ctx, CompletionRequest{
		Model: model,
		Context: LlmContext{Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		}},
		Config: StreamConfig{APIKey: "test-key"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}

	var text strings.Builder
	errored := false
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case StreamTextEvent:
			text.WriteString(fallbackTextOf(e.Partial))
		case StreamDoneEvent:
			text.WriteString(fallbackTextOf(e.Message))
		case StreamErrorEvent:
			errored = true
			t.Errorf("fallback surfaced an error: %v", e.Err)
		}
	}
	if errored {
		t.FailNow()
	}
	if got := text.String(); !strings.Contains(got, "pong") {
		mu.Lock()
		seen := append([]string(nil), paths...)
		mu.Unlock()
		t.Errorf("streamed text = %q, want pong (paths: %v)", got, seen)
	}
	mu.Lock()
	seen := append([]string(nil), paths...)
	mu.Unlock()
	want := []string{"/responses", "/messages", "/chat/completions"}
	if len(seen) != len(want) {
		t.Errorf("probed paths = %v, want %v", seen, want)
	} else {
		for i := range want {
			if seen[i] != want[i] {
				t.Errorf("probed paths = %v, want %v", seen, want)
				break
			}
		}
	}
	if w, ok := CachedWire("opencode-go", model); !ok || w != WireChat {
		t.Errorf("cached wire = %q,%v, want chat,true", w, ok)
	}
	if w, ok := CachedWire("opencode-go", "glm-5.2"); !ok || w != WireChat {
		t.Errorf("family cache = %q,%v, want chat,true", w, ok)
	}
}

// TestFallbackPromotesConfirmedWireOnce verifies the discovery is not repeated
// on every turn: after the first answer walks Responses → Messages → Chat and
// succeeds on Chat, the second request must go straight to Chat. The persistent
// wire cache covers the next resolution, but a running session reuses the same
// wrapper, so only an in-process promotion saves it from replaying the 400.
func TestFallbackPromotesConfirmedWireOnce(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_WIRE_PROBE", "off")
	resetWireCache()

	const model = "glm-5.3"
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/responses", "/messages":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol."}`)
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"`+model+`","choices":[{"delta":{"content":"pong"}}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"`+model+`","choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, _, err := ResolveNamedProvider("opencode-go", model, srv.URL, "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveNamedProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := CompletionRequest{
		Model: model,
		Context: LlmContext{Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		}},
		Config: StreamConfig{APIKey: "test-key"},
	}
	drain := func() {
		stream, err := p.StreamCompletion(ctx, req)
		if err != nil {
			t.Fatalf("StreamCompletion: %v", err)
		}
		var text strings.Builder
		for ev := range stream.Events() {
			switch e := ev.(type) {
			case StreamTextEvent:
				text.WriteString(fallbackTextOf(e.Partial))
			case StreamDoneEvent:
				text.WriteString(fallbackTextOf(e.Message))
			case StreamErrorEvent:
				t.Errorf("fallback surfaced an error: %v", e.Err)
			}
		}
		if !strings.Contains(text.String(), "pong") {
			t.Errorf("streamed text = %q, want pong", text.String())
		}
	}
	drain()
	drain()

	mu.Lock()
	seen := append([]string(nil), paths...)
	mu.Unlock()
	want := []string{"/responses", "/messages", "/chat/completions", "/chat/completions"}
	if len(seen) != len(want) {
		t.Fatalf("attempted paths = %v, want %v (second turn must reuse the confirmed wire)", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("attempted paths = %v, want %v", seen, want)
		}
	}
}

// TestFallbackWiresOrder pins the declared-wire chain: Responses → Messages →
// Chat → Gemini, starting after the resolved wire.
func TestFallbackWiresOrder(t *testing.T) {
	spec, ok := LookupProviderSpec("opencode-go")
	if !ok {
		t.Fatal("opencode-go spec missing")
	}
	cases := []struct {
		primary Wire
		want    []Wire
	}{
		{WireResponses, []Wire{WireMessages, WireChat, WireGemini}},
		{WireMessages, []Wire{WireChat, WireGemini}},
		{WireChat, []Wire{WireGemini}},
		{WireGemini, nil},
	}
	for _, c := range cases {
		got := fallbackWires(spec, c.primary)
		if len(got) != len(c.want) {
			t.Errorf("fallbackWires(%s) = %v, want %v", c.primary, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("fallbackWires(%s) = %v, want %v", c.primary, got, c.want)
				break
			}
		}
	}
}

// TestNonProtocolErrorIsNotMaskedByFallback reproduces the reported
// after-restart log: the gateway's Chat endpoint answered "model_coming_soon",
// but the wrapper fell through to Gemini and surfaced Gemini's generic
// ModelProtocolUnsupported instead. A non-protocol failure is the wire's real
// answer and must stop the chain.
func TestNonProtocolErrorIsNotMaskedByFallback(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_WIRE_PROBE", "off")
	resetWireCache()

	const model = "mimo-v2.6-flash"
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(w, `{"error":{"param":"model","type":"invalid_request_error","message":"Upstream request failed: [model_coming_soon] This model is coming soon."}}`)
		default:
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"ModelProtocolUnsupported","message":"Model does not support this protocol."}}`)
		}
	}))
	defer srv.Close()

	p, _, err := ResolveNamedProvider("opencode-go", model, srv.URL, "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveNamedProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := p.StreamCompletion(ctx, CompletionRequest{
		Model: model,
		Context: LlmContext{Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		}},
		Config: StreamConfig{APIKey: "test-key"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var gotErr string
	for ev := range stream.Events() {
		if se, ok := ev.(StreamErrorEvent); ok {
			gotErr = se.Err.Error()
		}
	}
	if !strings.Contains(gotErr, "model_coming_soon") {
		t.Errorf("surfaced error = %q, want the Chat model_coming_soon answer", gotErr)
	}
	mu.Lock()
	seen := append([]string(nil), paths...)
	mu.Unlock()
	if len(seen) != 1 || seen[0] != "/chat/completions" {
		t.Errorf("attempted paths = %v, want only /chat/completions", seen)
	}
	if w, ok := CachedWire("opencode-go", model); !ok || w != WireChat {
		t.Errorf("cached wire = %q,%v, want chat,true", w, ok)
	}
}

func fallbackTextOf(m agentcore.AssistantMessage) string {
	var b strings.Builder
	for _, c := range m.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestIsModelProtocolUnsupportedErr pins the narrow match: only the gateway's
// explicit protocol rejection qualifies.
func TestIsModelProtocolUnsupportedErr(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{io.EOF, false},
		{&fallbackTestErr{"400 Bad Request ModelProtocolUnsupported"}, true},
		{&fallbackTestErr{"Model does not support this protocol."}, true},
		{&fallbackTestErr{`Model "z-ai/glm-5.3-flash" is not available on this endpoint. Call it on /provider/v1/chat/completions instead.`}, true},
		{&fallbackTestErr{`Model "z-ai/glm-5.3-flash" is not supported on this endpoint. Use /provider/v1/chat/completions for OpenAI and OSS models.`}, true},
		{&fallbackTestErr{`Model "deepseek-v4.1-flash" is not supported on this endpoint.`}, false},
		{&fallbackTestErr{`{"code":"unsupported_model","message":"No such model"}`}, false},
		{&fallbackTestErr{"400 invalid tools"}, false},
		{nil, false},
	} {
		if got := isModelProtocolUnsupportedErr(c.err); got != c.want {
			t.Errorf("isModelProtocolUnsupportedErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

type fallbackTestErr struct{ s string }

func (e *fallbackTestErr) Error() string { return e.s }
