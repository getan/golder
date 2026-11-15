package mobilebridge

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// TestChatSSEContract pins the minimal OpenAI-compatible stream the Android
// instrumentation test's loopback server replays. If this host test passes and
// the device test still stalls, the remaining difference is in the Android
// process/bridge rather than the SSE shape.
func TestChatSSEContract(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from fake\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	prov, name, err := provider.ResolveProvider("fake-model", srv.URL+"/v1", "openai", "", func(string) string { return "" })
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	stream, err := provider.StreamFnFromProvider(prov)(context.Background(), "fake-model", provider.LlmContext{
		Messages: agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}}},
	}, provider.StreamConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var text strings.Builder
	for ev := range stream.Events() {
		if done, ok := ev.(provider.StreamDoneEvent); ok {
			text.WriteString(agentcore.ContentToText(done.Message.Content))
		}
	}
	final, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if !strings.Contains(text.String(), "hello from fake") {
		t.Fatalf("provider=%s text=%q final=%+v", name, text.String(), final)
	}
}
