package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/option"

	"github.com/getan/golder/internal/agentcore"
)

// roundTripFunc adapts a function to http.RoundTripper so a test can stub the
// SDK transport without a live endpoint.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newResponsesTestDriver builds a resp_api driver whose SDK client is pointed at
// the given stub round-tripper, capturing the request path the SDK targets.
func newResponsesTestDriver(baseURL string, rt roundTripFunc) *responsesDriver {
	d := NewOpenAIResponsesProvider("openai", baseURL, nil)
	d.clientOpts = []option.RequestOption{
		option.WithHTTPClient(&http.Client{Transport: rt}),
	}
	return d
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// sseResponse builds a 200 text/event-stream response whose body is the given
// SSE data frames, mirroring how the Responses API streams events. Each frame is
// a JSON object carrying its own "type" discriminator.
func sseResponse(frames ...string) *http.Response {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

// completedFrame is a response.completed SSE frame whose embedded Response yields
// the given output text, id, model, and token usage — the authoritative terminal
// payload the driver maps into its final message.
func completedFrame(text, id, model string, inTok, outTok int) string {
	return `{"type":"response.completed","sequence_number":99,"response":{` +
		`"id":"` + id + `","model":"` + model + `",` +
		`"output":[{"type":"message","role":"assistant","status":"completed",` +
		`"content":[{"type":"output_text","text":"` + text + `"}]}],` +
		`"usage":{"input_tokens":` + itoa(inTok) + `,"output_tokens":` + itoa(outTok) +
		`,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`
}

func deltaFrame(delta string) string {
	return `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,` +
		`"content_index":0,"sequence_number":1,"logprobs":[],"delta":"` + delta + `"}`
}

// emptyCompletedFrame is a response.completed event that carries nothing usable:
// no output items at all, and no usage either. It is the shape a gateway sends
// when it ends a turn without producing anything — the case the driver retries
// once, because the alternative is telling the user to check their model and
// protocol configuration over an upstream hiccup.
func emptyCompletedFrame(id, model string) string {
	return `{"type":"response.completed","sequence_number":1,"response":{` +
		`"id":"` + id + `","model":"` + model + `","output":[],` +
		`"usage":{"input_tokens":0,"output_tokens":0,` +
		`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`
}

func itoa(n int) string { return strconv.Itoa(n) }

// drain collects the terminal message from a stream, mirroring how the loop
// consumes a provider stream.
func drain(t *testing.T, stream *AssistantMessageEventStream) agentcore.AssistantMessage {
	t.Helper()
	for range stream.Events() {
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}
	return msg
}

func userMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

func TestResponsesDriverPostsToResponsesEndpoint(t *testing.T) {
	var gotPath, gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(
			deltaFrame("hi "),
			deltaFrame("there"),
			completedFrame("hi there", "resp_123", "gpt-4o", 11, 7),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	req := CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{
			SystemPrompt: "be terse",
			Messages:     agentcore.MessageList{userMsg("hello")},
		},
		Config: StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	if !strings.HasSuffix(gotPath, "/responses") {
		t.Errorf("request path = %q, want to end with /responses", gotPath)
	}
	// The prompt and system instruction must reach the wire body.
	if !strings.Contains(gotBody, "hello") {
		t.Errorf("request body missing prompt: %q", gotBody)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if payload["instructions"] != "be terse" {
		t.Errorf("instructions = %v, want %q", payload["instructions"], "be terse")
	}
	if payload["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", payload["model"])
	}
	// A streaming call must set stream:true on the wire.
	if payload["stream"] != true {
		t.Errorf("stream = %v, want true", payload["stream"])
	}

	if got := textOf(msg); got != "hi there" {
		t.Errorf("assistant text = %q, want %q", got, "hi there")
	}
	if msg.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", msg.StopReason)
	}
	if msg.ResponseID != "resp_123" {
		t.Errorf("response id = %q, want resp_123", msg.ResponseID)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 11 || msg.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want {11 7}", msg.Usage)
	}
	if msg.API != "openai" || msg.Provider != "openai" {
		t.Errorf("tags = api:%q provider:%q, want openai/openai", msg.API, msg.Provider)
	}
}

// The driver must emit incremental text partials as deltas arrive, and each
// partial must carry the text accumulated so far (not just the latest delta), so
// the terminal message equals the concatenation the caller already rendered.
func TestResponsesDriverStreamsIncrementalDeltas(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			deltaFrame("Hello"),
			deltaFrame(", "),
			deltaFrame("world"),
			completedFrame("Hello, world", "resp_9", "gpt-4o", 3, 4),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}

	var textPartials []string
	for ev := range stream.Events() {
		if te, ok := ev.(StreamTextEvent); ok {
			textPartials = append(textPartials, textOf(te.Partial))
		}
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}

	want := []string{"Hello", "Hello, ", "Hello, world"}
	if len(textPartials) != len(want) {
		t.Fatalf("got %d text partials %q, want %d %q", len(textPartials), textPartials, len(want), want)
	}
	for i := range want {
		if textPartials[i] != want[i] {
			t.Errorf("partial[%d] = %q, want %q", i, textPartials[i], want[i])
		}
	}
	// Final aggregation must match the completed payload, i.e. the last partial.
	if got := textOf(msg); got != "Hello, world" {
		t.Errorf("final text = %q, want %q", got, "Hello, world")
	}
}

// An empty completion must be retried once, because the failure is usually
// upstream: a gateway ends the turn with no text, no reasoning and no tool call.
// The user saw "check that --model, --base-url and --protocol match the same
// provider" and went looking for a configuration mistake that was not there.
func TestResponsesDriverRetriesAnEmptyCompletion(t *testing.T) {
	var calls int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return sseResponse(emptyCompletedFrame("resp_empty", "gpt-4o")), nil
		}
		return sseResponse(completedFrame("recovered", "resp_ok", "gpt-4o", 5, 3)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	if calls != 2 {
		t.Errorf("requests = %d, want 2 (one empty attempt and one retry)", calls)
	}
	if got := textOf(msg); got != "recovered" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 5 {
		t.Errorf("usage = %+v, want the retry's", msg.Usage)
	}
}

// The retry is bounded: two empty completions in a row are surfaced as they
// were before. Retrying forever would turn an upstream fault into a hang, and a
// model that keeps ending its turn with nothing is not a problem another
// request fixes.
func TestResponsesDriverDoesNotRetryAnEmptyCompletionTwice(t *testing.T) {
	var calls int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return sseResponse(emptyCompletedFrame("resp_empty", "gpt-4o")), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	if calls != 2 {
		t.Errorf("requests = %d, want exactly 2 before giving up", calls)
	}
	// The empty message still reaches the caller, so the front-end keeps its
	// note — the difference is that one transient empty no longer produces it.
	if len(msg.Content) != 0 {
		t.Errorf("content = %v, want the empty result to pass through", msg.Content)
	}
}

// A cancelled context must terminate the stream with a terminal event rather
// than yielding a normal end_turn message. The transport cancels mid-flight
// (after the stream has started) and reports the cancellation, mirroring how an
// in-progress SSE read aborts when the caller cancels.
//
// The stop reason is aborted, not error: a cancelled context means the caller
// interrupted (that is the only way this path is reached in practice), and
// filing a keystroke under error is what made every front-end print an error
// line for Ctrl+C. The terminal event is still a StreamErrorEvent — it rides
// the same channel and ends the stream the same way — only its classification
// differs.
func TestResponsesDriverContextCancelStopsStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(ctx, CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error on cancel: %v", err)
	}

	var sawError bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamErrorEvent); ok {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected a terminal StreamErrorEvent after context cancel")
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonAborted {
		t.Errorf("stop reason = %q, want aborted (a cancelled ctx is an interrupt)", msg.StopReason)
	}
	if msg.ErrorMessage != "" {
		t.Errorf("error message = %q, want empty (the front-end prints its own interrupt notice)", msg.ErrorMessage)
	}
}

// A non-2xx from the endpoint must ride the stream as a terminal error event,
// not be returned from StreamCompletion (dual failure model, FR-13).
func TestResponsesDriverUpstreamErrorRidesStream(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	req := CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hello")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	}
	stream, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error on upstream failure: %v", err)
	}

	var sawError bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamErrorEvent); ok {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected a terminal StreamErrorEvent for a 401 response")
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// An in-band error event (type "error") mid-stream must ride the stream as a
// terminal error, carrying the event's message. This is a distinct path from a
// transport-level non-2xx (which surfaces via the stream's Err()).
func TestResponsesDriverInStreamErrorEvent(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			deltaFrame("partial"),
			`{"type":"error","code":"server_error","message":"boom","param":"","sequence_number":2}`,
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error: %v", err)
	}

	var errEvent *StreamErrorEvent
	for ev := range stream.Events() {
		if se, ok := ev.(StreamErrorEvent); ok {
			e := se
			errEvent = &e
		}
	}
	if errEvent == nil {
		t.Fatal("expected a terminal StreamErrorEvent for an in-band error event")
	}
	if !strings.Contains(errEvent.Message.ErrorMessage, "boom") {
		t.Errorf("error message = %q, want to contain %q", errEvent.Message.ErrorMessage, "boom")
	}
	if !isStreamCutErr(errEvent.Err) {
		t.Errorf("error = %v, want a retryable upstream cut (the retry wrapper needs it)", errEvent.Err)
	}
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// A missing API key is the one early "cannot build the stream" error.
func TestResponsesDriverMissingKeyIsEarlyError(t *testing.T) {
	d := NewOpenAIResponsesProvider("openai", "https://api.openai.test/v1", nil)
	_, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:  "gpt-4o",
		Config: StreamConfig{APIKey: "  "},
	})
	if err == nil {
		t.Fatal("expected early error for missing API key")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error = %q, want to mention missing API key", err.Error())
	}
}

// textOf returns the concatenated text content of an assistant message.
func textOf(m agentcore.AssistantMessage) string {
	var b bytes.Buffer
	for _, c := range m.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// fakeTool is a minimal AgentTool for exercising tool-schema serialization; it
// never executes in these transport-level tests.
type fakeTool struct {
	name   string
	desc   string
	schema json.RawMessage
}

func (t fakeTool) Name() string                               { return t.name }
func (t fakeTool) Description() string                        { return t.desc }
func (t fakeTool) Schema() json.RawMessage                    { return t.schema }
func (t fakeTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (t fakeTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

// functionCallDoneFrame is a response.output_item.done SSE frame carrying a
// finalized function_call item (the model's tool request).
func functionCallDoneFrame(callID, name, argsJSON string) string {
	frame := map[string]any{
		"type":            "response.output_item.done",
		"output_index":    0,
		"sequence_number": 5,
		"item": map[string]any{
			"type":      "function_call",
			"id":        "fc_1",
			"call_id":   callID,
			"name":      name,
			"arguments": argsJSON,
			"status":    "completed",
		},
	}
	b, _ := json.Marshal(frame)
	return string(b)
}

// completedToolFrame is a response.completed frame whose output is a single
// function_call item (no assistant text) plus token usage.
func completedToolFrame(id, model, callID, name, argsJSON string) string {
	frame := map[string]any{
		"type":            "response.completed",
		"sequence_number": 99,
		"response": map[string]any{
			"id":    id,
			"model": model,
			"output": []any{map[string]any{
				"type":      "function_call",
				"id":        "fc_1",
				"call_id":   callID,
				"name":      name,
				"arguments": argsJSON,
				"status":    "completed",
			}},
			"usage": map[string]any{
				"input_tokens":          5,
				"output_tokens":         2,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			},
		},
	}
	b, _ := json.Marshal(frame)
	return string(b)
}

// A golder tool must reach the wire as a Responses function tool: type "function",
// its name, JSON-Schema parameters, and description.
func TestResponsesDriverSendsToolSchema(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("ok", "resp_1", "gpt-4o", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	tool := fakeTool{
		name:   "read_file",
		desc:   "reads a file",
		schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{
			Messages: agentcore.MessageList{userMsg("read a.go")},
			Tools:    []agentcore.AgentTool{tool},
		},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("tools = %v, want [hosted web_search, function]", payload["tools"])
	}
	if tools[0].(map[string]any)["type"] != "web_search_preview" {
		t.Errorf("tools[0].type = %v, want web_search_preview (hosted leads)", tools[0])
	}
	tool0 := tools[1].(map[string]any)
	if tool0["type"] != "function" {
		t.Errorf("tool type = %v, want function", tool0["type"])
	}
	if tool0["name"] != "read_file" {
		t.Errorf("tool name = %v, want read_file", tool0["name"])
	}
	if tool0["description"] != "reads a file" {
		t.Errorf("tool description = %v, want %q", tool0["description"], "reads a file")
	}
	params, ok := tool0["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("tool parameters missing or not an object: %v", tool0["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok || props["path"] == nil {
		t.Errorf("tool parameters.properties.path missing: %v", params)
	}
}

// TestResponsesDriverAllowsParallelToolCalls: the request must explicitly
// permit several tool calls in one answer (golder runs that batch in
// parallel). OpenAI defaults the field on; compatible Responses gateways do
// not always, so golder sends it like codex does.
func TestResponsesDriverAllowsParallelToolCalls(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("ok", "resp_1", "gpt-4o", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if got, ok := payload["parallel_tool_calls"].(bool); !ok || !got {
		t.Fatalf("parallel_tool_calls = %v, want true", payload["parallel_tool_calls"])
	}
}

// A function_call in the completed response must be parsed into a golder
// ToolCallContent (id + name + raw arguments) and set StopReason=tool_use; a
// StreamToolCallEvent must also surface the pending call mid-stream.
func TestResponsesDriverParsesToolCall(t *testing.T) {
	args := `{"path":"a.go"}`
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			functionCallDoneFrame("call_abc", "read_file", args),
			completedToolFrame("resp_7", "gpt-4o", "call_abc", "read_file", args),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("read a.go")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}

	var sawToolCallEvent bool
	for ev := range stream.Events() {
		if _, ok := ev.(StreamToolCallEvent); ok {
			sawToolCallEvent = true
		}
	}
	if !sawToolCallEvent {
		t.Error("expected a StreamToolCallEvent mid-stream")
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result error: %v", err)
	}

	calls := msg.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if calls[0].ID != "call_abc" || calls[0].Name != "read_file" {
		t.Errorf("tool call = id:%q name:%q, want call_abc/read_file", calls[0].ID, calls[0].Name)
	}
	if string(calls[0].Arguments) != args {
		t.Errorf("tool call arguments = %q, want %q", calls[0].Arguments, args)
	}
	if msg.StopReason != agentcore.StopReasonToolUse {
		t.Errorf("stop reason = %q, want tool_use", msg.StopReason)
	}
}

// On a follow-up turn, a prior assistant tool call must be replayed as a
// function_call input item and its result as a function_call_output item, both
// keyed by the same call_id, so the model can pair request and result.
func TestResponsesDriverBackfillsToolResult(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("done", "resp_2", "gpt-4o", 8, 3)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	assistant := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewToolCallContent("call_abc", "read_file", json.RawMessage(`{"path":"a.go"}`)),
		},
	}
	result := agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: "call_abc",
		ToolName:   "read_file",
		Content:    agentcore.ContentList{agentcore.NewTextContent("package main")},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{
			userMsg("read a.go"), assistant, result,
		}},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	var sawCall, sawOutput bool
	for _, item := range payload.Input {
		switch item["type"] {
		case "function_call":
			sawCall = true
			if item["call_id"] != "call_abc" || item["name"] != "read_file" {
				t.Errorf("function_call item = %v, want call_abc/read_file", item)
			}
			if item["arguments"] != `{"path":"a.go"}` {
				t.Errorf("function_call arguments = %v, want the raw args JSON string", item["arguments"])
			}
		case "function_call_output":
			sawOutput = true
			if item["call_id"] != "call_abc" {
				t.Errorf("function_call_output call_id = %v, want call_abc", item["call_id"])
			}
			if item["output"] != "package main" {
				t.Errorf("function_call_output output = %v, want %q", item["output"], "package main")
			}
		}
	}
	if !sawCall {
		t.Error("wire input missing the replayed function_call item")
	}
	if !sawOutput {
		t.Error("wire input missing the function_call_output item")
	}
}

// completedReasoningFrame is a response.completed frame whose output carries a
// reasoning item (summary text) followed by the assistant message text.
func completedReasoningFrame(id, model, summary, text string) string {
	frame := map[string]any{
		"type":            "response.completed",
		"sequence_number": 99,
		"response": map[string]any{
			"id":    id,
			"model": model,
			"output": []any{
				map[string]any{
					"type": "reasoning",
					"id":   "rs_1",
					"summary": []any{map[string]any{
						"type": "summary_text",
						"text": summary,
					}},
				},
				map[string]any{
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []any{map[string]any{
						"type": "output_text",
						"text": text,
					}},
				},
			},
			"usage": map[string]any{
				"input_tokens":          3,
				"output_tokens":         4,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		},
	}
	b, _ := json.Marshal(frame)
	return string(b)
}

// A user message carrying an image must reach the wire as a message whose
// content is a part list: an input_text part plus an input_image part whose
// image_url is the base64 data URI.
func TestResponsesDriverSendsImageInput(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("ok", "resp_1", "gpt-4o", 1, 1)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	imgMsg := agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{
			agentcore.NewTextContent("what is this?"),
			agentcore.NewImageContent("aGVsbG8=", "image/png"),
		},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{imgMsg}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload struct {
		Input []struct {
			Type    string           `json:"type"`
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if len(payload.Input) != 1 {
		t.Fatalf("got %d input items, want 1", len(payload.Input))
	}
	parts := payload.Input[0].Content
	var sawText, sawImage bool
	for _, p := range parts {
		switch p["type"] {
		case "input_text":
			sawText = true
			if p["text"] != "what is this?" {
				t.Errorf("input_text = %v, want %q", p["text"], "what is this?")
			}
		case "input_image":
			sawImage = true
			if p["image_url"] != "data:image/png;base64,aGVsbG8=" {
				t.Errorf("input_image image_url = %v, want the data URI", p["image_url"])
			}
		}
	}
	if !sawText {
		t.Error("wire input missing the input_text part")
	}
	if !sawImage {
		t.Error("wire input missing the input_image part")
	}
}

// A request with a thinking level must set the reasoning.effort (and an auto
// summary) on the wire, and a reasoning item in the completed response must be
// parsed into a leading ThinkingContent block.
func TestResponsesDriverReasoning(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedReasoningFrame("resp_9", "gpt-4o", "let me think", "the answer")), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("solve it")}},
		Config:  StreamConfig{APIKey: "sk-test", ThinkingLevel: agentcore.ThinkingMedium},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	msg := drain(t, stream)

	var payload struct {
		Reasoning struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if payload.Reasoning.Effort != "medium" {
		t.Errorf("reasoning.effort = %q, want medium", payload.Reasoning.Effort)
	}
	if payload.Reasoning.Summary != "auto" {
		t.Errorf("reasoning.summary = %q, want auto", payload.Reasoning.Summary)
	}

	var thinking string
	for _, c := range msg.Content {
		if tc, ok := c.(agentcore.ThinkingContent); ok {
			thinking = tc.Thinking
		}
	}
	if thinking != "let me think" {
		t.Errorf("thinking content = %q, want %q", thinking, "let me think")
	}
}

// The reasoning.summary parameter only goes to backends that accept it: codex's
// catalogs mark opencode-go and DeepSeek supports_reasoning_summary=false, and a
// summary requested there only spends server-side work on text the reply never
// carries. Every backend still receives the effort itself.
// Every Responses request that carries a reasoning effort also asks for an auto
// summary: that is the field that carries the model's reasoning back (muse
// streams reasoning_summary_text deltas only when it is requested; without it
// the summary comes back empty), and golder renders it as a thinking block.
// codex's catalogs mark OpenCode Go and DeepSeek as not supporting the parameter
// and codex omits it there, but live probes show the gateway honors it, so
// golder deliberately diverges to keep thinking visible.
func TestResponsesDriverAlwaysRequestsReasoningSummary(t *testing.T) {
	for _, tc := range []struct {
		provider string
		model    string
	}{
		{"openai", "gpt-4o"},
		{"opencode-go", "muse-spark-1.3-contributor"},
		{"deepseek", "deepseek-chat"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var gotBody string
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Body != nil {
					b, _ := io.ReadAll(r.Body)
					gotBody = string(b)
				}
				return sseResponse(completedFrame("ok", "resp_1", tc.model, 5, 1)), nil
			})
			d := NewOpenAIResponsesProvider(tc.provider, "https://api.openai.test/v1", nil)
			d.clientOpts = []option.RequestOption{
				option.WithHTTPClient(&http.Client{Transport: rt}),
			}

			stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
				Model:   tc.model,
				Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
				Config:  StreamConfig{APIKey: "sk-test", ThinkingLevel: agentcore.ThinkingMedium},
			})
			if err != nil {
				t.Fatalf("StreamCompletion returned early error: %v", err)
			}
			drain(t, stream)

			var payload struct {
				Reasoning map[string]any `json:"reasoning"`
			}
			if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
				t.Fatalf("request body not valid JSON: %v", err)
			}
			if payload.Reasoning["effort"] != "medium" {
				t.Errorf("reasoning.effort = %v, want medium", payload.Reasoning["effort"])
			}
			if payload.Reasoning["summary"] != "auto" {
				t.Errorf("reasoning.summary = %v, want auto (body: %s)", payload.Reasoning["summary"], gotBody)
			}
		})
	}
}

// The session id doubles as prompt_cache_key so the backend can bucket one
// conversation's prefix cache; without a session id the field stays off the
// wire entirely.
func TestResponsesDriverPromptCacheKey(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"session id", map[string]any{ExtraSessionID: "sid-1"}, "sid-1"},
		{"blank session id", map[string]any{ExtraSessionID: "  "}, ""},
		{"no extra", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Body != nil {
					b, _ := io.ReadAll(r.Body)
					gotBody = string(b)
				}
				return sseResponse(completedFrame("ok", "resp_1", "gpt-4o", 5, 1)), nil
			})
			d := newResponsesTestDriver("https://api.openai.test/v1", rt)

			stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
				Model:   "gpt-4o",
				Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
				Config:  StreamConfig{APIKey: "sk-test", Extra: tc.extra},
			})
			if err != nil {
				t.Fatalf("StreamCompletion returned early error: %v", err)
			}
			drain(t, stream)

			var payload map[string]any
			if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
				t.Fatalf("request body not valid JSON: %v", err)
			}
			got, present := payload["prompt_cache_key"]
			if tc.want == "" {
				if present {
					t.Errorf("prompt_cache_key = %v, want the field omitted", got)
				}
				return
			}
			if !present || got != tc.want {
				t.Errorf("prompt_cache_key = %v (present=%v), want %q", got, present, tc.want)
			}
		})
	}
}

// Display-only ThinkingContent is not the provider's protocol state. It must
// never be fabricated into a reasoning item on replay; only an original item
// from the same provider/model (or the explicit missing-reasoning placeholder,
// tested separately below) may become a reasoning input item.
func TestResponsesDriverDoesNotReplayDisplayOnlyThinking(t *testing.T) {
	var gotBody string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		return sseResponse(completedFrame("next", "resp_3", "gpt-4o", 9, 2)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	assistant := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("hidden chain of thought"),
			agentcore.NewTextContent("the answer"),
		},
	}
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model: "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{
			userMsg("ask"), assistant,
		}},
		Config: StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}
	drain(t, stream)

	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	var sawAssistantText bool
	for _, item := range payload.Input {
		if item["type"] == "reasoning" {
			t.Errorf("input replayed a reasoning item: %v", item)
		}
		// Easy input messages carry no "type" on the wire, only role/content.
		if item["role"] == "assistant" {
			if item["content"] != "the answer" {
				t.Errorf("assistant input content = %v, want %q", item["content"], "the answer")
			}
			sawAssistantText = true
		}
	}
	if !sawAssistantText {
		t.Error("assistant text missing from the replayed input")
	}
	if strings.Contains(gotBody, "hidden chain of thought") || strings.Contains(gotBody, "encrypted_content") {
		t.Errorf("reasoning payload leaked into the request body: %s", gotBody)
	}
}

// The Responses replay has ONE shape for every backend: an assistant tool-call
// step leads with its reasoning and then replays its calls as one contiguous
// run. Both DeepSeek endpoints (the first-party API and OpenCode Go, probed
// live) enforce that shape from two sides — a call run with no non-empty
// reasoning_text head answers "The `reasoning_text` in the thinking mode must be
// passed back to the API", while an item placed INSIDE the run ends the run as
// far as the gateway is concerned and answers "No tool output found for tool
// call …" for a call whose output is present. The loosest backend (the OpenAI
// relay) accepts the same body unchanged, head included.
//
// The head is the step's captured items replayed verbatim, plus the single
// explicit placeholder when those items cannot satisfy the check — an item
// whose reasoning_text is empty, or one that carries only a summary, does not.
// There is never one placeholder per call.
func TestResponsesReasoningHeadAndContiguousCalls(t *testing.T) {
	const deepseek = "deepseek-v4.1-flash"
	original := `{"type":"reasoning","id":"rs_original","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"original private reasoning"}]}`
	emptyText := `{"type":"reasoning","id":"rs_empty","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":""}]}`
	summaryOnly := `{"type":"reasoning","id":"rs_summary","summary":[{"type":"summary_text","text":"summary, not reasoning_text"}]}`
	cases := []struct {
		name                        string
		provider, model             string
		originProvider, originModel string
		reasoning                   []json.RawMessage
		level                       agentcore.ThinkingLevel
		// originMarker is a substring unique to the captured item; wantOrigin
		// says whether the replay may include it.
		originMarker    string
		wantOrigin      bool
		wantPlaceholder bool
	}{
		{name: "captured reasoning is the head", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: deepseek,
			reasoning: []json.RawMessage{json.RawMessage(original)}, level: agentcore.ThinkingMax,
			originMarker: "original private reasoning", wantOrigin: true},
		{name: "no captured reasoning gets one placeholder", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: deepseek,
			level: agentcore.ThinkingMax, wantPlaceholder: true},
		{name: "empty reasoning_text is replayed beside the placeholder", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: deepseek,
			reasoning: []json.RawMessage{json.RawMessage(emptyText)}, level: agentcore.ThinkingMax,
			originMarker: "rs_empty", wantOrigin: true, wantPlaceholder: true},
		{name: "summary-only item is replayed beside the placeholder", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: deepseek,
			reasoning: []json.RawMessage{json.RawMessage(summaryOnly)}, level: agentcore.ThinkingMax,
			originMarker: "summary, not reasoning_text", wantOrigin: true, wantPlaceholder: true},
		{name: "another model's opaque item is never replayed", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: "muse-spark-1.3",
			reasoning: []json.RawMessage{json.RawMessage(original)}, level: agentcore.ThinkingMax,
			originMarker: "original private reasoning", wantPlaceholder: true},
		{name: "another provider's opaque item is never replayed", provider: "opencode-go", model: deepseek,
			originProvider: "openai", originModel: deepseek,
			reasoning: []json.RawMessage{json.RawMessage(original)}, level: agentcore.ThinkingMax,
			originMarker: "original private reasoning", wantPlaceholder: true},
		{name: "thinking off takes the bare shape", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: deepseek},
		{name: "thinking off still replays a captured item verbatim", provider: "opencode-go", model: deepseek,
			originProvider: "opencode-go", originModel: deepseek,
			reasoning:    []json.RawMessage{json.RawMessage(original)},
			originMarker: "original private reasoning", wantOrigin: true},
		{name: "the same shape serves every gateway", provider: "openai", model: "gpt-4o",
			originProvider: "openai", originModel: "gpt-4o", level: agentcore.ThinkingMedium, wantPlaceholder: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				sent, _ = io.ReadAll(r.Body)
				return sseResponse(completedFrame("ok", "resp_new", tc.model, 10, 5)), nil
			})
			d := NewOpenAIResponsesProvider(tc.provider, "https://api.openai.test/v1", nil)
			d.clientOpts = []option.RequestOption{option.WithHTTPClient(&http.Client{Transport: rt})}
			assistant := agentcore.AssistantMessage{
				RoleField: agentcore.RoleAssistant, Provider: tc.originProvider, ResponseModel: tc.originModel,
				ReasoningItems: tc.reasoning,
				Content: agentcore.ContentList{
					agentcore.NewThinkingContent("display summary is not protocol state"),
					agentcore.NewToolCallContent("call_one", "read", json.RawMessage(`{"path":"a.go"}`)),
					agentcore.NewToolCallContent("call_two", "read", json.RawMessage(`{"path":"b.go"}`)),
				},
			}
			stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
				Model: tc.model, Context: LlmContext{Messages: agentcore.MessageList{
					userMsg("read two files"), assistant,
					agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call_one", Content: agentcore.ContentList{agentcore.NewTextContent("a")}},
					agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call_two", Content: agentcore.ContentList{agentcore.NewTextContent("b")}},
				}}, Config: StreamConfig{APIKey: "sk-test", ThinkingLevel: tc.level},
			})
			if err != nil {
				t.Fatal(err)
			}
			drain(t, stream)

			var wire struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(sent, &wire); err != nil {
				t.Fatalf("invalid request: %v", err)
			}

			// The expected item sequence: the user message, the head (the
			// captured items and/or the placeholder), then the two calls and
			// their outputs as two contiguous runs.
			kinds := make([]string, 0, len(wire.Input))
			for _, item := range wire.Input {
				kind, _ := item["type"].(string)
				if kind == "" {
					kind = "message" // an easy input message carries role/content only
				}
				kinds = append(kinds, kind)
			}
			want := "message"
			if tc.wantOrigin {
				want += ",reasoning"
			}
			if tc.wantPlaceholder {
				want += ",reasoning"
			}
			want += ",function_call,function_call,function_call_output,function_call_output"
			if got := strings.Join(kinds, ","); got != want {
				t.Errorf("input items = %s, want %s; wire=%s", got, want, sent)
			}

			// A thinking-mode step that replays calls must offer at least one
			// NON-EMPTY reasoning_text part: an empty one is what the old
			// per-call marker sent, and both DeepSeek endpoints read it as
			// absent.
			var replayed strings.Builder
			for i, item := range wire.Input {
				if kinds[i] != "reasoning" {
					continue
				}
				parts, _ := item["content"].([]any)
				for _, p := range parts {
					pm, _ := p.(map[string]any)
					if pm["type"] == "reasoning_text" {
						part, _ := pm["text"].(string)
						replayed.WriteString(part)
					}
				}
			}
			if thinkingOn := tc.level != "" && tc.level != agentcore.ThinkingOff; thinkingOn &&
				strings.TrimSpace(replayed.String()) == "" {
				t.Errorf("thinking-mode call step offers no non-empty reasoning_text; wire=%s", sent)
			}
			if tc.originMarker != "" {
				if got := strings.Contains(string(sent), tc.originMarker); got != tc.wantOrigin {
					t.Errorf("captured item replayed = %v, want %v; wire=%s", got, tc.wantOrigin, sent)
				}
			}
			if got := strings.Contains(string(sent), "original reasoning for this step was not recorded"); got != tc.wantPlaceholder {
				t.Errorf("placeholder replayed = %v, want %v; wire=%s", got, tc.wantPlaceholder, sent)
			}
			if strings.Contains(string(sent), "display summary is not protocol state") {
				t.Errorf("must not replay display-only thinking: %s", sent)
			}
		})
	}
}

// Regression for the reported failure: the history of the session whose request
// was rejected with `No tool output found for tool call
// call_00_XyWjxdCQYfX9QBLp13yW6601` (opencode-go / deepseek-v4.1-flash, one
// assistant step with two parallel calls). The replay then put an empty
// reasoning marker between the two calls; the gateway reads a reasoning item
// inside the run as the run having ended, so it reported a call whose output is
// present as unanswered. The step must replay as one contiguous run led by its
// reasoning — the captured original when there is one, the explicit placeholder
// otherwise.
func TestResponsesReplaysReportedDeepSeekStepContiguously(t *testing.T) {
	const (
		provider = "opencode-go"
		model    = "deepseek-v4.1-flash"
		callA    = "call_00_XyWjxdCQYfX9QBLp13yW6601"
		callB    = "call_01_FPaYgIKFJqEaUazzHKOF4327"
	)
	// The reasoning item the gateway returned for that step (text trimmed).
	reasoning := json.RawMessage(`{"id":"4cc941ca-81fa-47e3-a9cf-c16f5881b2b9","type":"reasoning","status":"completed","encrypted_content":"4ba4a834-bf85-4b27-9539-54aa2f856fd8-0","content":[{"type":"reasoning_text","text":"Let me get oriented. The task list has items, some done."}],"summary":[]}`)
	for _, tc := range []struct {
		name            string
		items           []json.RawMessage
		wantPlaceholder bool
	}{
		{name: "reasoning captured", items: []json.RawMessage{reasoning}},
		{name: "reasoning not recorded", wantPlaceholder: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				sent, _ = io.ReadAll(r.Body)
				return sseResponse(completedFrame("ok", "resp_2", model, 10, 5)), nil
			})
			d := NewOpenAIResponsesProvider(provider, "https://opencode.ai/zen/go/v1", nil)
			d.clientOpts = []option.RequestOption{option.WithHTTPClient(&http.Client{Transport: rt})}

			assistant := agentcore.AssistantMessage{
				RoleField: agentcore.RoleAssistant, Provider: provider, ResponseModel: model,
				ReasoningItems: tc.items,
				Content: agentcore.ContentList{
					agentcore.NewThinkingContent("Let me get oriented. The task list has items, some done."),
					agentcore.NewToolCallContent(callA, "bash", json.RawMessage(`{"command":"pwd; ls"}`)),
					agentcore.NewToolCallContent(callB, "memory_search", json.RawMessage(`{"query":"responses 400"}`)),
				},
			}
			stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
				Model: model, Context: LlmContext{Messages: agentcore.MessageList{
					userMsg("continue"),
					assistant,
					agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: callA, ToolName: "bash", Content: agentcore.ContentList{agentcore.NewTextContent("workspace listing")}},
					agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: callB, ToolName: "memory_search", Content: agentcore.ContentList{agentcore.NewTextContent("one result")}},
				}}, Config: StreamConfig{APIKey: "sk-test", ThinkingLevel: agentcore.ThinkingMax},
			})
			if err != nil {
				t.Fatal(err)
			}
			drain(t, stream)

			var wire struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(sent, &wire); err != nil {
				t.Fatalf("invalid request: %v", err)
			}
			var kinds []string
			for _, item := range wire.Input {
				kind, _ := item["type"].(string)
				if kind == "" {
					kind = "message"
				}
				kinds = append(kinds, kind)
			}
			want := "message,reasoning,function_call,function_call,function_call_output,function_call_output"
			if got := strings.Join(kinds, ","); got != want {
				t.Fatalf("input items = %s, want %s (nothing may sit inside the call run); wire=%s", got, want, sent)
			}
			// The head must carry non-empty reasoning_text: an empty part is
			// exactly what the failing request sent and the gateway rejected.
			content, _ := wire.Input[1]["content"].([]any)
			if len(content) != 1 {
				t.Fatalf("reasoning head content = %v, want one reasoning_text part", wire.Input[1])
			}
			part, _ := content[0].(map[string]any)
			text, _ := part["text"].(string)
			if part["type"] != "reasoning_text" || strings.TrimSpace(text) == "" {
				t.Errorf("reasoning head = %v, want a non-empty reasoning_text part", wire.Input[1])
			}
			if got := strings.Contains(string(sent), "original reasoning for this step was not recorded"); got != tc.wantPlaceholder {
				t.Errorf("placeholder replayed = %v, want %v; wire=%s", got, tc.wantPlaceholder, sent)
			}
		})
	}
}

func TestResponsesStoresReasoningTextFromCompletedResponse(t *testing.T) {
	raw := `{"type":"response.completed","sequence_number":10,"response":{"id":"resp_1","model":"deepseek-v4.1-flash","output":[{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"work through the question"}]},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}]}}`
	d := newResponsesTestDriver("https://api.openai.test/v1", func(*http.Request) (*http.Response, error) { return sseResponse(raw), nil })
	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{Model: "deepseek-v4.1-flash", Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}}, Config: StreamConfig{APIKey: "sk-test"}})
	if err != nil {
		t.Fatal(err)
	}
	msg := drain(t, stream)
	if len(msg.ReasoningItems) != 1 || !strings.Contains(string(msg.ReasoningItems[0]), `"reasoning_text"`) {
		t.Fatalf("completed reasoning item lost: %+v", msg)
	}
	if len(msg.Content) == 0 || msg.Content[0].(agentcore.ThinkingContent).Thinking != "work through the question" {
		t.Fatalf("reasoning_text was not displayed as thinking: %+v", msg.Content)
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var stored agentcore.AssistantMessage
	if err := json.Unmarshal(encoded, &stored); err != nil || len(stored.ReasoningItems) != 1 {
		t.Fatalf("reasoning item not preserved in session history: err=%v items=%v", err, stored.ReasoningItems)
	}
}

// A response.failed event must surface the server's embedded error
// (code + message + id), not a bare "response failed" that cannot be
// diagnosed.
func TestResponsesDriverFailedEventCarriesServerError(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sseResponse(
			deltaFrame("partial"),
			`{"type":"response.failed","sequence_number":2,"response":{"id":"resp-9","error":{"code":"server_error","message":"upstream exploded"}}}`,
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion should not early-error: %v", err)
	}

	var errEvent *StreamErrorEvent
	for ev := range stream.Events() {
		if se, ok := ev.(StreamErrorEvent); ok {
			e := se
			errEvent = &e
		}
	}
	if errEvent == nil {
		t.Fatal("expected a terminal StreamErrorEvent for response.failed")
	}
	for _, want := range []string{"resp-9", "server_error", "upstream exploded"} {
		if !strings.Contains(errEvent.Message.ErrorMessage, want) {
			t.Errorf("error message = %q, want to contain %q", errEvent.Message.ErrorMessage, want)
		}
	}
	if !isStreamCutErr(errEvent.Err) {
		t.Errorf("error = %v, want a retryable upstream cut (server_error is transient)", errEvent.Err)
	}
	if got := streamCutReason(errEvent.Err); got != StreamCutUpstream {
		t.Errorf("reason = %q, want %q", got, StreamCutUpstream)
	}
}

// The reported failure end to end: the first Responses attempt ends with
// response.failed / server_error before any output, the retry completes, and
// the caller sees only the recovered answer plus a notice naming the upstream
// failure.
func TestResponsesServerErrorIsRetriedBeforeContent(t *testing.T) {
	fastRetryBackoff(t)
	var calls int32
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return sseResponse(
				`{"type":"response.failed","sequence_number":1,"response":{"id":"resp-1","error":{"code":"server_error","message":"An error occurred while processing your request."}}}`,
			), nil
		}
		return sseResponse(completedFrame("recovered", "resp-2", "gpt-4o", 3, 4)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := withStreamRetry(d, WireResponses).StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}

	var (
		text    string
		errText string
		notices []StreamNoticeEvent
	)
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case StreamDoneEvent:
			text = agentcore.ContentToText(e.Message.Content)
		case StreamErrorEvent:
			errText = e.Message.ErrorMessage
		case StreamNoticeEvent:
			notices = append(notices, e)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("attempts = %d, want 2 (the failed response and one retry)", got)
	}
	if errText != "" {
		t.Errorf("error = %q, want none: the retry answered", errText)
	}
	if text != "recovered" {
		t.Errorf("text = %q, want the retry's answer", text)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %d (%+v), want exactly 1", len(notices), notices)
	}
	if notices[0].Reason != StreamCutUpstream {
		t.Errorf("notice reason = %q, want %q", notices[0].Reason, StreamCutUpstream)
	}
	if !strings.Contains(notices[0].Detail, "server_error") {
		t.Errorf("notice detail = %q, want the server's code", notices[0].Detail)
	}
}

// A deterministic failure code is not retryable: the wrapper must leave it
// terminal even though response.failed is the same event the server_error path
// uses.
func TestResponsesFailedEventRejectionIsNotACut(t *testing.T) {
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return sseResponse(
			`{"type":"response.failed","sequence_number":1,"response":{"id":"resp-3","error":{"code":"invalid_prompt","message":"the prompt was rejected"}}}`,
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var errEvent *StreamErrorEvent
	for ev := range stream.Events() {
		if se, ok := ev.(StreamErrorEvent); ok {
			errEvent = &se
		}
	}
	if errEvent == nil {
		t.Fatal("expected the rejection as a terminal StreamErrorEvent")
	}
	if isStreamCutErr(errEvent.Err) {
		t.Errorf("error = %v, want a non-retryable rejection", errEvent.Err)
	}
}
