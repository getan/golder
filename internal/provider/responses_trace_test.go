package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/diagtrace"
	"github.com/openai/openai-go/option"
)

func TestResponsesTraceRoundTripDoesNotLeakProtocolContent(t *testing.T) {
	diagtrace.Close()
	path := filepath.Join(t.TempDir(), "trace.log")
	if err := diagtrace.Init(path); err != nil {
		t.Fatal(err)
	}
	defer diagtrace.Close()
	const raw = `{"type":"reasoning","id":"PRIVATE_REASONING_ID","content":[{"type":"thinking","thinking":"PRIVATE_THINKING","signature":"PRIVATE_SIGNATURE"}]}`
	const call = `{"type":"function_call","id":"PRIVATE_CALL_ID","call_id":"PRIVATE_CALL_ID","name":"read","arguments":"{\"path\":\"PRIVATE_PATH\"}"}`
	d := NewOpenAIResponsesProvider("opencode-go", "https://PRIVATE_GATEWAY.test/v1", nil)
	requests := 0
	d.clientOpts = []option.RequestOption{option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return sseResponse(
				`{"type":"response.created","response":{"id":"PRIVATE_RESPONSE_ID","model":"PRIVATE_RESOLVED_MODEL","output":[]}}`,
				`{"type":"response.output_item.done","output_index":0,"item":`+raw+`}`,
				`{"type":"response.output_item.done","output_index":1,"item":`+call+`}`,
				`{"type":"response.completed","response":{"id":"PRIVATE_RESPONSE_ID","model":"PRIVATE_RESOLVED_MODEL","output":[`+call+`]}}`,
			), nil
		}
		return jsonResponse(400, `{"error":{"message":"The content[].thinking in the thinking mode must be passed back to the API. PRIVATE_ERROR_TOKEN"}}`), nil
	})})}
	req := CompletionRequest{Model: "deepseek-v4.1-flash", Context: LlmContext{Messages: agentcore.MessageList{userMsg("PRIVATE_PROMPT")}}, Config: StreamConfig{APIKey: "PRIVATE_API_KEY", ThinkingLevel: agentcore.ThinkingHigh}}
	s, err := d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	first := drain(t, s)
	req.Context.Messages = append(req.Context.Messages, first, agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "PRIVATE_CALL_ID", Content: agentcore.ContentList{agentcore.NewTextContent("PRIVATE_TOOL_RESULT")}})
	before, _ := json.Marshal(buildResponsesParams(d.name, req, false))
	after, _ := json.Marshal(buildResponsesParamsTraced(d.name, req, false, nextResponsesTrace()))
	if string(before) != string(after) {
		t.Fatal("diagnostics changed request JSON")
	}
	s, err = d.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = drain(t, s)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"responses request=", "capture completed=true finalized=1 terminal=0 stored=1", "thinking_parts=1", "model_alias=true",
		"replay step=1 stored=1 selected=1 disposition=replayed model_source=requested", "head_nonempty=true placeholder=false local_calls=1",
		"error_class=thinking_replay_rejected http_status=400",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in trace", want)
		}
	}
	if strings.Contains(text, "PRIVATE_") || strings.Contains(text, req.Model) || strings.Contains(text, "must be passed back") {
		t.Fatal("structural trace leaked request/response content")
	}
}

func TestResponsesTraceReplayDispositionsAndDisabled(t *testing.T) {
	diagtrace.Close()
	before := responsesTraceSequence.Load()
	if nextResponsesTrace() != 0 || responsesTraceSequence.Load() != before {
		t.Fatal("disabled tracing allocated a request identifier")
	}
	path := filepath.Join(t.TempDir(), "trace.log")
	if err := diagtrace.Init(path); err != nil {
		t.Fatal(err)
	}
	defer diagtrace.Close()
	const model = "deepseek-v4.1-flash"
	for _, tc := range []struct {
		provider, originModel, responseModel, item, want string
	}{
		{"opencode-go", model, "alias", `{"type":"reasoning","content":[{"type":"thinking","thinking":"PRIVATE_THOUGHT"}]}`, "disposition=replayed model_source=requested"},
		{"other", model, model, `{"type":"reasoning"}`, "disposition=provider_mismatch"},
		{"opencode-go", "other", model, `{"type":"reasoning"}`, "disposition=model_mismatch"},
		{"opencode-go", "", model, `{"type":"reasoning"}`, "disposition=replayed model_source=legacy_response"},
		{"opencode-go", model, model, `{"type":"unknown","value":"PRIVATE_CONTENT"}`, "disposition=no_valid_items"},
		{"opencode-go", model, model, "", "disposition=not_recorded"},
	} {
		msg := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Provider: tc.provider, Model: tc.originModel, ResponseModel: tc.responseModel,
			Content: agentcore.ContentList{agentcore.NewToolCallContent("PRIVATE_ID", "PRIVATE_TOOL", json.RawMessage(`{}`))}}
		if tc.item != "" {
			msg.ReasoningItems = []json.RawMessage{json.RawMessage(tc.item)}
		}
		req := CompletionRequest{Model: model, Context: LlmContext{Messages: agentcore.MessageList{msg}}, Config: StreamConfig{ThinkingLevel: agentcore.ThinkingHigh}}
		plain, _ := json.Marshal(buildResponsesParams("opencode-go", req, false))
		traced, _ := json.Marshal(buildResponsesParamsTraced("opencode-go", req, false, nextResponsesTrace()))
		if string(plain) != string(traced) {
			t.Fatal("logging changed replay selection")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), tc.want) || strings.Contains(string(data), "PRIVATE_") {
			t.Errorf("missing safe disposition %s", tc.want)
		}
	}
}
