package provider

// This file pins the thinking-shape recovery path: a model newer than the
// provider catalog (the one-shot-run case, where models.dev is never fetched)
// gets one corrected request instead of a failed turn. The gateway answers 400
// naming the adaptive form, the driver re-issues the same request with that
// body, and the verdict is remembered for the rest of the process.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// resetEffortThinkingVerdicts clears the in-process memory of gateways that
// rejected the legacy thinking body, so a test neither inherits nor leaks one.
func resetEffortThinkingVerdicts(t *testing.T) {
	t.Helper()
	clear := func() {
		effortThinkingVerdicts.Range(func(k, _ any) bool {
			effortThinkingVerdicts.Delete(k)
			return true
		})
	}
	clear()
	t.Cleanup(clear)
}

// effortOnlyServer answers a legacy token-budget thinking body with the 400 the
// Messages API sends for an adaptive-only model, and any other body with a
// normal stream. Every request body it sees is recorded, so a test can count
// attempts and read what was sent.
func effortOnlyServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if strings.Contains(string(raw), `"enabled"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Upstream request failed: [invalid_request_error] \"thinking.type.enabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior."}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicToolUseSSE))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// TestAnthropicThinkingShapeRecovery drives the whole path: no catalog entry, so
// the driver sends the legacy body; the gateway refuses it, naming the adaptive
// form; the request is re-issued once and the turn succeeds. The next request on
// the same driver goes straight to the adaptive body.
func TestAnthropicThinkingShapeRecovery(t *testing.T) {
	resetEffortThinkingVerdicts(t)
	srv, bodies := effortOnlyServer(t)
	p := NewAnthropicProtocolProvider("recovery-gw", srv.URL, AuthBearer, nil)

	req := func() CompletionRequest {
		return CompletionRequest{
			Model: "brand-new-model",
			Context: LlmContext{Messages: agentcore.MessageList{
				agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
			}},
			Config: StreamConfig{APIKey: "k", ThinkingLevel: agentcore.ThinkingHigh},
		}
	}

	stream, err := p.StreamCompletion(context.Background(), req())
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	kinds, final := drainStream(t, stream)
	if len(kinds) == 0 || kinds[len(kinds)-1] != StreamEventDone {
		t.Fatalf("stream did not complete: kinds=%v err=%q", kinds, final.ErrorMessage)
	}

	if len(*bodies) != 2 {
		t.Fatalf("requests = %d, want 2 (the legacy attempt, then the corrected one)", len(*bodies))
	}
	if !strings.Contains((*bodies)[0], `"enabled"`) {
		t.Errorf("first attempt should carry the legacy body:\n%s", (*bodies)[0])
	}
	corrected := (*bodies)[1]
	if !strings.Contains(corrected, `"adaptive"`) || strings.Contains(corrected, `"budget_tokens"`) {
		t.Errorf("corrected attempt should carry the adaptive body:\n%s", corrected)
	}
	if !strings.Contains(corrected, `"output_config"`) || !strings.Contains(corrected, `"high"`) {
		t.Errorf("corrected attempt should name the effort level:\n%s", corrected)
	}

	// The verdict is remembered: the same driver does not repeat the 400.
	before := len(*bodies)
	stream, err = p.StreamCompletion(context.Background(), req())
	if err != nil {
		t.Fatalf("second StreamCompletion: %v", err)
	}
	for range stream.Events() {
	}
	if len(*bodies) != before+1 {
		t.Errorf("second turn made %d requests, want 1: the rejected shape must be remembered", len(*bodies)-before)
	}
	if last := (*bodies)[len(*bodies)-1]; !strings.Contains(last, `"adaptive"`) {
		t.Errorf("second turn should go straight to the adaptive body:\n%s", last)
	}
}

// TestAnthropicThinkingShapeRecoveryIgnoresUnrelatedBadRequest: a 400 that does
// not name the adaptive shape is not retried — one request, and the upstream
// error surfaces unchanged.
func TestAnthropicThinkingShapeRecoveryIgnoresUnrelatedBadRequest(t *testing.T) {
	resetEffortThinkingVerdicts(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content: bad tool schema"}}`))
	}))
	defer srv.Close()

	p := NewAnthropicProtocolProvider("recovery-gw-unrelated", srv.URL, AuthBearer, nil)
	_, err := p.StreamCompletion(context.Background(), CompletionRequest{
		Model: "brand-new-model",
		Context: LlmContext{Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		}},
		Config: StreamConfig{APIKey: "k", ThinkingLevel: agentcore.ThinkingHigh},
	})
	if err == nil {
		t.Fatal("an unrelated 400 should surface as an error")
	}
	if len(bodies) != 1 {
		t.Errorf("requests = %d, want 1 (no correction for an unrelated 400)", len(bodies))
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad tool schema") {
		t.Errorf("error = %q, want the upstream 400 verbatim", err)
	}
}
