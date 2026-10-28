package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/openai/openai-go/option"
)

// Exercise capture -> session serialization -> a real second wire request.
// Supplying hand-built ReasoningItems to appendInputItems cannot detect state
// lost by the SSE decoder or a gateway returning a resolved model alias.
func TestResponsesThinkingSurvivesActualToolRoundTrip(t *testing.T) {
	const model = "deepseek-v4.1-flash"
	const reasoning = `{"type":"reasoning","id":"rs_step","summary":[],"content":[{"type":"reasoning_text","text":"original protocol reasoning"}],"encrypted_content":"opaque-step-state"}`
	const thinking = `{"type":"reasoning","id":"rs_step","summary":[],"content":[{"type":"thinking","thinking":"original protocol thinking","signature":"opaque-signature"}]}`
	const call = `{"type":"function_call","id":"fc_step","call_id":"call_step","name":"bash","arguments":"{\"command\":\"printf fixture\"}"}`
	for _, tc := range []struct {
		name          string
		rawReasoning  string
		responseModel string
		terminal      bool
		terminalItems string
	}{
		{"item_done_not_repeated_in_completed", reasoning, model, true, call},
		{"clean_stream_with_finalized_items_only", reasoning, model, false, ""},
		{"gateway_resolved_model_alias", reasoning, "deepseek-resolved-backend", true, reasoning + "," + call},
		{"gateway_thinking_content_part", thinking, model, true, thinking + "," + call},
		{"completed_repeats_only_empty_reasoning_shell", thinking, model, true, `{"type":"reasoning","id":"rs_step","summary":[]},` + call},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			d := NewOpenAIResponsesProvider("opencode-go", "https://gateway.test/v1", nil)
			d.clientOpts = []option.RequestOption{option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					frames := []string{
						`{"type":"response.created","sequence_number":0,"response":{"id":"resp_step","model":"` + tc.responseModel + `","output":[]}}`,
						`{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":` + tc.rawReasoning + `}`,
						`{"type":"response.output_item.done","sequence_number":2,"output_index":1,"item":` + call + `}`,
					}
					if tc.terminal {
						frames = append(frames, `{"type":"response.completed","sequence_number":3,"response":{"id":"resp_step","model":"`+tc.responseModel+`","output":[`+tc.terminalItems+`]}}`)
					}
					return sseResponse(frames...), nil
				}
				body, _ := io.ReadAll(r.Body)
				var wire struct {
					Input []json.RawMessage `json:"input"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				matches := 0
				for _, raw := range wire.Input {
					if sameRawJSON(raw, json.RawMessage(tc.rawReasoning)) {
						matches++
					}
				}
				if matches != 1 || strings.Contains(string(body), "original reasoning for this step was not recorded") {
					t.Errorf("original reasoning must be replayed exactly once, without a fabricated replacement: %s", body)
					return jsonResponse(400, `{"error":{"code":"invalid_request_error","message":"The content[].thinking in the thinking mode must be passed back to the API."}}`), nil
				}
				return sseResponse(completedFrame("verified continuation", "resp_final", model, 1, 1)), nil
			})})}
			request := CompletionRequest{Model: model, Context: LlmContext{Messages: agentcore.MessageList{userMsg("run fixture")}}, Config: StreamConfig{APIKey: "fixture", ThinkingLevel: agentcore.ThinkingHigh}}
			stream, err := d.StreamCompletion(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			first := drain(t, stream)
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			var stored agentcore.AssistantMessage
			if err = json.Unmarshal(encoded, &stored); err != nil {
				t.Fatal(err)
			}
			request.Context.Messages = append(request.Context.Messages, stored, agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call_step", ToolName: "bash", Content: agentcore.ContentList{agentcore.NewTextContent("fixture")}})
			stream, err = d.StreamCompletion(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			final := drain(t, stream)
			if final.StopReason == agentcore.StopReasonError || contentText(final.Content) != "verified continuation" || requests != 2 {
				t.Fatalf("tool continuation failed: %+v, requests=%d", final, requests)
			}
		})
	}
}

func sameRawJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

func TestResponsesRequestModelProvenanceDoesNotFollowBackendAlias(t *testing.T) {
	const raw = `{"type":"reasoning","id":"private_step","content":[{"type":"thinking","thinking":"do not send to a different selection"}]}`
	message := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant, Provider: "opencode-go", Model: "selected-model-a", ResponseModel: "resolved-model-b",
		ReasoningItems: []json.RawMessage{json.RawMessage(raw)},
		Content:        agentcore.ContentList{agentcore.NewToolCallContent("call", "read", json.RawMessage(`{}`))},
	}
	for _, target := range []struct {
		provider string
		model    string
		wantRaw  bool
	}{
		{"opencode-go", "selected-model-a", true},
		{"opencode-go", "resolved-model-b", false},
		{"another-provider", "selected-model-a", false},
	} {
		params := buildResponsesParams(target.provider, CompletionRequest{Model: target.model, Context: LlmContext{Messages: agentcore.MessageList{message}}}, false)
		body, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(body), "do not send to a different selection"); got != target.wantRaw {
			t.Fatalf("provider=%s model=%s raw state replay=%t, want %t", target.provider, target.model, got, target.wantRaw)
		}
	}
}

func TestResponsesRecoveryPreservesReasoningOrderAndDeduplicates(t *testing.T) {
	a := json.RawMessage(`{"type":"reasoning","id":"a","content":[{"type":"reasoning_text","text":"first"}]}`)
	b := json.RawMessage(`{"type":"reasoning","id":"b","content":[{"type":"reasoning_text","text":"second"}]}`)
	got := mergeFinalizedReasoning([]json.RawMessage{b}, []json.RawMessage{a, b})
	if len(got) != 2 || !sameRawJSON(got[0], a) || !sameRawJSON(got[1], b) {
		t.Fatalf("missing earlier reasoning was reordered/duplicated: %s", got)
	}
}
