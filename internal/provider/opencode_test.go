package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/openai/openai-go/option"
)

func TestSessionHeaderValue(t *testing.T) {
	extra := map[string]any{ExtraSessionID: "sess-123"}
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"opencode-go", extra, "sess-123"},
		{"opencode", extra, "sess-123"},
		{"openai", extra, ""},
		{"anthropic", extra, ""},
		{"opencode-go", nil, ""},
		{"opencode-go", map[string]any{}, ""},
		{"opencode-go", map[string]any{ExtraSessionID: "  "}, ""},
	}
	for _, c := range cases {
		if got := SessionHeaderValue(c.name, c.extra); got != c.want {
			t.Errorf("SessionHeaderValue(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWithSessionExtra(t *testing.T) {
	// Nil map + id yields a map carrying the id.
	got := WithSessionExtra(nil, "s1")
	if got[ExtraSessionID] != "s1" {
		t.Errorf("nil map: got %v, want session s1", got)
	}
	// Empty id is a no-op (nil stays nil).
	if out := WithSessionExtra(nil, ""); out != nil {
		t.Errorf("empty sid: got %v, want nil", out)
	}
	// An existing id always wins; the input map is not mutated.
	in := map[string]any{ExtraSessionID: "orig", "k": 1}
	out := WithSessionExtra(in, "new")
	if out[ExtraSessionID] != "orig" {
		t.Errorf("existing id overwritten: %v", out[ExtraSessionID])
	}
	if len(in) != 2 {
		t.Errorf("input map mutated: %v", in)
	}
}

func TestChatDriverOpencodeSessionHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(OpencodeSessionHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	mkDriver := func(name string) *openAICompatDriver {
		return &openAICompatDriver{name: name, baseURL: srv.URL, models: []Model{{Provider: name, ID: "m"}}}
	}
	req := func() CompletionRequest {
		return CompletionRequest{Model: "m", Config: StreamConfig{Extra: map[string]any{ExtraSessionID: "sess-9"}}}
	}

	// opencode-wired driver sends the header.
	stream, err := mkDriver("opencode-go").StreamCompletion(context.Background(), req())
	if err != nil {
		t.Fatalf("opencode-go stream: %v", err)
	}
	for range stream.Events() {
	}
	if gotHeader != "sess-9" {
		t.Errorf("opencode-go header = %q, want sess-9", gotHeader)
	}

	// Other providers never send it.
	gotHeader = "unset"
	stream, err = mkDriver("openai").StreamCompletion(context.Background(), req())
	if err != nil {
		t.Fatalf("openai stream: %v", err)
	}
	for range stream.Events() {
	}
	if gotHeader != "" {
		t.Errorf("openai header = %q, want empty", gotHeader)
	}
}

func TestResponsesDriverOpencodeSessionHeader(t *testing.T) {
	var gotHeader string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Get(OpencodeSessionHeader)
		return sseResponse(completedFrame("hi", "resp-1", "m", 1, 1)), nil
	})
	d := NewOpenAIResponsesProvider("opencode-go", "https://example.test/v1", nil)
	d.clientOpts = []option.RequestOption{
		option.WithHTTPClient(&http.Client{Transport: rt}),
	}

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:  "m",
		Config: StreamConfig{APIKey: "test-key", Extra: map[string]any{ExtraSessionID: "sess-7"}},
	})
	if err != nil {
		t.Fatalf("responses stream: %v", err)
	}
	for range stream.Events() {
	}
	if gotHeader != "sess-7" {
		t.Errorf("responses header = %q, want sess-7", gotHeader)
	}
}

func TestClientForURL(t *testing.T) {
	t.Setenv("PIGO_PROXY", "http://127.0.0.1:7897")
	// Registry flag wins regardless of URL (covers --base-url overrides).
	if c := clientForURL("opencode-go", "https://custom.example.com/v1/responses"); c == nil {
		t.Error("opencode-go with custom URL = nil, want proxied client")
	}
	// Hostname fallback for callers without a provider name.
	for _, u := range []string{
		"https://opencode.ai/zen/go/v1/responses",
		"https://opencode.ai/zen/v1/chat/completions",
		"https://opencode.ai/zen/go/v1/models",
	} {
		if c := clientForURL("", u); c == nil {
			t.Errorf("clientForURL(%q) = nil, want proxied client", u)
		}
	}
	for _, u := range []string{
		"https://api.openai.com/v1/responses",
		"https://api.anthropic.com/v1/messages",
		"http://localhost:11434/v1/chat/completions",
	} {
		if c := clientForURL("openai", u); c != nil {
			t.Errorf("clientForURL(%q) = %v, want nil", u, c)
		}
	}
	if c := clientForURL("openai", "::not-a-url::://"); c != nil {
		t.Errorf("bad URL: got %v, want nil", c)
	}
	// Explicitly disabled proxying.
	t.Setenv("PIGO_PROXY", "")
	if c := clientForURL("opencode-go", "https://opencode.ai/zen/go/v1/responses"); c != nil {
		t.Errorf("disabled proxy: got %v, want nil", c)
	}
}

func TestProxyURL(t *testing.T) {
	t.Setenv("PIGO_PROXY", "http://proxy.internal:8080")
	if got := ProxyURL(); got != "http://proxy.internal:8080" {
		t.Errorf("ProxyURL() = %q, want override", got)
	}
	t.Setenv("PIGO_PROXY", "")
	if got := ProxyURL(); got != "" {
		t.Errorf("ProxyURL() = %q, want empty (disabled)", got)
	}
}

func TestProxyURLDefaultsDirect(t *testing.T) {
	old, had := os.LookupEnv("PIGO_PROXY")
	if err := os.Unsetenv("PIGO_PROXY"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("PIGO_PROXY", old)
		}
	})
	if got := ProxyURL(); got != "" {
		t.Errorf("unset ProxyURL() = %q, want empty (direct)", got)
	}
	if c := clientForURL("opencode-go", "https://opencode.ai/zen/go/v1/responses"); c != nil {
		t.Errorf("unset proxy client = %v, want nil (direct)", c)
	}
}
