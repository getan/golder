//go:build android

package mobilebridge

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/mobileagent"
	"github.com/getan/golder/internal/provider"
)

// TestAndroidChatSSEContract runs the same provider/SSE contract on the device
// with a Go loopback server, separating Android's Go HTTP path from the Kotlin
// fake server used by the instrumentation test.
func TestAndroidChatSSEContract(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if _, err := stream.Result(context.Background()); err != nil {
		t.Fatalf("result: %v", err)
	}
	if !strings.Contains(text.String(), "hello from fake") {
		t.Fatalf("provider=%s text=%q", name, text.String())
	}
}

// TestAndroidBackendWithGoServer runs the exact Android backend (toolset,
// provider, mobileagent loop) against a Go loopback SSE server, without JNI or
// the Kotlin fake server.
func TestAndroidBackendWithGoServer(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from fake\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	backend, err := NewAndroidSession(Config{
		Workspace: t.TempDir(),
		Model:     "fake-model",
		Protocol:  "openai",
		BaseURL:   srv.URL + "/v1",
		APIKey:    "test-key",
	})
	if err != nil {
		t.Fatalf("NewAndroidSession: %v", err)
	}
	defer backend.Close()
	if err := backend.Start("hello"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var text strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := backend.PollJSON(100)
		if err != nil {
			t.Fatalf("PollJSON: %v", err)
		}
		var events []mobileagent.Event
		if err := json.Unmarshal([]byte(raw), &events); err != nil {
			t.Fatalf("decode events: %v", err)
		}
		for _, ev := range events {
			switch ev.Type {
			case mobileagent.EventText:
				text.WriteString(ev.Text)
			case mobileagent.EventRunEnd:
				if ev.Error != "" {
					t.Fatalf("run error: %s", ev.Error)
				}
				if !strings.Contains(text.String(), "hello from fake") {
					t.Fatalf("text = %q", text.String())
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run did not end; text=%q", text.String())
}
