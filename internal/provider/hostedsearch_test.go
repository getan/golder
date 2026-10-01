package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

// webSearchCallFrame builds a response.output_item.done SSE frame for a
// completed web_search_call output item carrying the given search query.
func webSearchCallFrame(id, query string) string {
	return `{"type":"response.output_item.done","sequence_number":3,` +
		`"output_index":0,"item":{"type":"web_search_call","id":"` + id + `",` +
		`"status":"completed","action":{"type":"search","query":"` + query + `"}}}`
}

// completedSearchFrame builds a response.completed frame whose output holds a
// web_search_call item plus an assistant message with one url_citation.
func completedSearchFrame(id, model, callID, query, text, title, url string) string {
	return `{"type":"response.completed","sequence_number":99,"response":{` +
		`"id":"` + id + `","model":"` + model + `",` +
		`"output":[` +
		`{"type":"web_search_call","id":"` + callID + `","status":"completed",` +
		`"action":{"type":"search","query":"` + query + `"}},` +
		`{"type":"message","role":"assistant","status":"completed",` +
		`"content":[{"type":"output_text","text":"` + text + `",` +
		`"annotations":[{"type":"url_citation","title":"` + title + `","url":"` + url + `",` +
		`"start_index":0,"end_index":4}]}]}],` +
		`"usage":{"input_tokens":3,"output_tokens":2,` +
		`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`
}

// toolTypes extracts the "type" of each entry in the wire tools array.
func toolTypes(t *testing.T, body string) []string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	raw, ok := payload["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing: %v", payload["tools"])
	}
	var out []string
	for _, e := range raw {
		m, _ := e.(map[string]any)
		ty, _ := m["type"].(string)
		out = append(out, ty)
	}
	return out
}

func toolNames(t *testing.T, body string) []string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	var out []string
	for _, e := range p["tools"].([]any) {
		m, _ := e.(map[string]any)
		if m["type"] == "function" {
			name, _ := m["name"].(string)
			out = append(out, name)
		}
	}
	return out
}

// The local websearch function tool must be hidden while the hosted variant is
// declared, so the model faces exactly one search path per attempt.
func TestHostedSearchHidesLocalWebsearch(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("ok", "resp_1", "m-grok", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	tools := []agentcore.AgentTool{
		fakeTool{name: "read_file", schema: json.RawMessage(`{"type":"object"}`)},
		fakeTool{name: "websearch", schema: json.RawMessage(`{"type":"object"}`)},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "probe-hide-model",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}, Tools: tools},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	if tys := toolTypes(t, gotBody); len(tys) != 2 || tys[0] != "web_search_preview" || tys[1] != "function" {
		t.Errorf("wire tool types = %v, want [web_search_preview function]", tys)
	}
	for _, n := range toolNames(t, gotBody) {
		if n == "websearch" {
			t.Errorf("local websearch must be hidden while hosted is declared (names=%v)", toolNames(t, gotBody))
		}
	}
}

// A capability-shaped 400 (unknown web_search tool) must retry once without
// the hosted tool (local websearch restored) and cache the negative: the next
// turn for the same (provider, model) skips the probe.
func TestHostedSearchCapabilityFallback(t *testing.T) {
	var bodies []string
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
		}
		if calls == 1 {
			return jsonResponse(http.StatusBadRequest,
				`{"error":{"message":"Unknown tool type: 'web_search_preview'","type":"invalid_request_error"}}`), nil
		}
		return sseResponse(completedFrame("fell back", "resp_2", "probe-fb-model", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)
	const model = "probe-fb-model"

	tools := []agentcore.AgentTool{
		fakeTool{name: "websearch", schema: json.RawMessage(`{"type":"object"}`)},
	}
	req := CompletionRequest{
		Model:   model,
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("search?")}, Tools: tools},
		Config:  StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)
	if got := textOf(msg); got != "fell back" {
		t.Errorf("assistant text = %q, want %q", got, "fell back")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 (probe + fallback)", calls)
	}
	if tys := toolTypes(t, bodies[0]); len(tys) != 1 || tys[0] != "web_search_preview" {
		t.Errorf("first attempt tools = %v, want [web_search_preview]", tys)
	}
	if tys := toolTypes(t, bodies[1]); len(tys) != 1 || tys[0] != "function" {
		t.Errorf("fallback tools = %v, want [function websearch]", tys)
	}
	if names := toolNames(t, bodies[1]); len(names) != 1 || names[0] != "websearch" {
		t.Errorf("fallback function names = %v, want [websearch]", names)
	}
	if !hostedSearchKnownUnsupported("openai", model) {
		t.Error("negative probe result should be cached")
	}

	// Second turn: single request, straight to the local tool.
	bodies = nil
	calls = 0
	rt2 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
		}
		return sseResponse(completedFrame("cached", "resp_3", model, 1, 1)), nil
	})
	d2 := newResponsesTestDriver("https://api.openai.test/v1", rt2)
	stream, err = d2.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)
	if calls != 1 {
		t.Fatalf("cached turn upstream calls = %d, want 1 (no probe)", calls)
	}
	if tys := toolTypes(t, bodies[0]); len(tys) != 1 || tys[0] != "function" {
		t.Errorf("cached turn tools = %v, want [function]", tys)
	}
	clearHostedSearchUnsupported("openai", model)
}

// A non-capability failure (e.g. 401) must surface as-is with no retry.
func TestHostedSearchNoRetryOnOtherErrors(t *testing.T) {
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "probe-401-model",
		Context: LlmContext{
			Messages: agentcore.MessageList{userMsg("hi")},
			Tools:    []agentcore.AgentTool{fakeTool{name: "read_file"}},
		},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error: %v", err)
	}
	var sawError bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamErrorEvent); ok {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected a terminal StreamErrorEvent")
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry on 401)", calls)
	}
}

// A completed web_search_call must surface as a server-executed call (never a
// local execution) and its citations must land in a Sources block.
func TestHostedSearchCallParsedAsServer(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			webSearchCallFrame("ws_1", "deepseek news"),
			completedSearchFrame("resp_9", "probe-ws-model", "ws_1", "deepseek news",
				"fresh news", "Example", "https://example.com/a"),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "probe-ws-model",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("news?")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	calls := msg.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1 server call", len(calls))
	}
	if calls[0].Name != "web_search" || !calls[0].IsServer() {
		t.Errorf("call = %+v, want server web_search", calls[0])
	}
	if !strings.Contains(string(calls[0].Arguments), "deepseek news") {
		t.Errorf("call arguments = %s, want the query", calls[0].Arguments)
	}
	if msg.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn (no local execution)", msg.StopReason)
	}
	text := textOf(msg)
	if !strings.Contains(text, "fresh news") {
		t.Errorf("answer text = %q, want the model text", text)
	}
	var joined string
	for _, c := range msg.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			joined += tc.Text + "\n"
		}
	}
	if !strings.Contains(joined, "https://example.com/a") {
		t.Errorf("missing Sources block with citation URL; content=%q", joined)
	}
}

// A follow-up turn must replay a server web_search call as a native
// web_search_call item (search action round-trips).
func TestHostedSearchCallReplayedNatively(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("done", "resp_10", "probe-rp-model", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	args, _ := json.Marshal(map[string]any{"query": "deepseek news"})
	asst := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewTextContent("fresh news"),
			agentcore.NewServerToolCallContent("ws_1", "web_search", args),
		},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "probe-rp-model",
		Context: LlmContext{Messages: agentcore.MessageList{
			userMsg("news?"),
			asst,
		}},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	if !strings.Contains(gotBody, `"web_search_call"`) {
		t.Errorf("replay body missing web_search_call item: %s", gotBody)
	}
	if !strings.Contains(gotBody, "deepseek news") {
		t.Errorf("replay body missing the query: %s", gotBody)
	}
	if strings.Contains(gotBody, `"function_call"`) {
		t.Errorf("server call must not replay as function_call: %s", gotBody)
	}
}

// A follow-up turn citing an already-surfaced URL must not re-print it; only
// genuinely new links join the Sources block.
func TestHostedSearchCitationsDedupedAcrossTurns(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			completedSearchFrame("resp_11", "probe-dd-model", "ws_2", "more news",
				"update", "Example", "https://example.com/a"),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	history := agentcore.MessageList{
		userMsg("news?"),
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("fresh news\nSources:\n- Example: https://example.com/a"),
			},
		},
		userMsg("and more?"),
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "probe-dd-model",
		Context: LlmContext{Messages: history},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	var joined string
	for _, c := range msg.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			joined += tc.Text + "\n"
		}
	}
	if strings.Contains(joined, "Sources:") {
		t.Errorf("already-cited URL must not re-print Sources, got:\n%s", joined)
	}
	if !strings.Contains(joined, "update") {
		t.Errorf("answer text must survive dedup, got:\n%s", joined)
	}
}
