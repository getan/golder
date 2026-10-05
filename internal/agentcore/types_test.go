package agentcore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContentListRoundTrip(t *testing.T) {
	in := ContentList{
		NewTextContent("hello"),
		NewThinkingContent("pondering"),
		NewToolCallContent("call_1", "read", json.RawMessage(`{"path":"a.go"}`)),
		NewImageContent("YmFzZTY0", "image/png"),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ContentList
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 blocks, got %d", len(out))
	}
	if _, ok := out[0].(TextContent); !ok {
		t.Errorf("block 0: want TextContent, got %T", out[0])
	}
	if _, ok := out[1].(ThinkingContent); !ok {
		t.Errorf("block 1: want ThinkingContent, got %T", out[1])
	}
	tc, ok := out[2].(ToolCallContent)
	if !ok {
		t.Fatalf("block 2: want ToolCallContent, got %T", out[2])
	}
	if tc.ID != "call_1" || tc.Name != "read" {
		t.Errorf("toolCall fields lost: %+v", tc)
	}
	if string(tc.Arguments) != `{"path":"a.go"}` {
		t.Errorf("arguments lost: %s", tc.Arguments)
	}
	if _, ok := out[3].(ImageContent); !ok {
		t.Errorf("block 3: want ImageContent, got %T", out[3])
	}
}

func TestContentUnknownTypeRejected(t *testing.T) {
	var out ContentList
	err := json.Unmarshal([]byte(`[{"type":"bogus"}]`), &out)
	if err == nil {
		t.Fatal("expected error for unknown content type")
	}
}

// TestServerToolCallKindRoundTrips locks the persistence marker for
// provider-executed calls: a session must remember that web_search ran
// provider-side, or a reload turns it into an ordinary dangling local call
// (synthetic repair results) and history replay loses the native item.
func TestServerToolCallKindRoundTrips(t *testing.T) {
	in := ContentList{NewServerToolCallContent("ws_1", "web_search", json.RawMessage(`{"query":"news"}`))}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"kind":"server"`) {
		t.Fatalf("marshaled server call lost its kind marker: %s", data)
	}
	var out ContentList
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tc, ok := out[0].(ToolCallContent)
	if !ok {
		t.Fatalf("block 0: want ToolCallContent, got %T", out[0])
	}
	if !tc.IsServer() {
		t.Errorf("reloaded call = %+v, want IsServer() true", tc)
	}
	// A local call keeps the empty marker (omitempty) so old files read the
	// same as before.
	local, err := json.Marshal(NewToolCallContent("c1", "read", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatalf("marshal local: %v", err)
	}
	if strings.Contains(string(local), `"kind"`) {
		t.Errorf("local call should not carry a kind field: %s", local)
	}
}

// TestToolCallInvalidArgumentsMarshal verifies a ToolCallContent whose
// Arguments are syntactically invalid JSON (as a model can stream) still
// marshals — as a JSON string of the raw bytes — rather than aborting the
// encode. Without this, session persistence and provider re-serialization would
// crash the whole turn on a single malformed tool call.
func TestToolCallInvalidArgumentsMarshal(t *testing.T) {
	bad := NewToolCallContent("c1", "todo", json.RawMessage(`{"todos": []{}"content": ""x"}`))
	data, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal invalid tool args: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("marshaled output is not valid JSON: %s", data)
	}
	// It must round-trip back through the discriminated decoder without error.
	var out ContentList
	if err := json.Unmarshal([]byte("["+string(data)+"]"), &out); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	tc, ok := out[0].(ToolCallContent)
	if !ok {
		t.Fatalf("want ToolCallContent, got %T", out[0])
	}
	// The raw invalid text is preserved (as the decoded string).
	var recovered string
	if err := json.Unmarshal(tc.Arguments, &recovered); err != nil {
		t.Fatalf("arguments not a JSON string: %v", err)
	}
	if recovered != `{"todos": []{}"content": ""x"}` {
		t.Errorf("raw arguments lost: %q", recovered)
	}
}

// TestToolCallValidArgumentsUnchanged verifies well-formed arguments are emitted
// verbatim (not string-wrapped), preserving the object shape providers expect.
func TestToolCallValidArgumentsUnchanged(t *testing.T) {
	tc := NewToolCallContent("c1", "read", json.RawMessage(`{"path":"a.go"}`))
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ContentList
	if err := json.Unmarshal([]byte("["+string(data)+"]"), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := out[0].(ToolCallContent)
	if string(got.Arguments) != `{"path":"a.go"}` {
		t.Errorf("arguments = %s, want the object unchanged", got.Arguments)
	}
}

func TestContentMissingTypeRejected(t *testing.T) {
	var out ContentList
	err := json.Unmarshal([]byte(`[{"text":"no type"}]`), &out)
	if err == nil {
		t.Fatal("expected error for missing type discriminant")
	}
}

func TestMessageListRoundTrip(t *testing.T) {
	term := true
	_ = term
	in := MessageList{
		UserMessage{RoleField: RoleUser, Content: ContentList{NewTextContent("hi")}, Timestamp: 1},
		AssistantMessage{
			RoleField:  RoleAssistant,
			Content:    ContentList{NewTextContent("ok"), NewToolCallContent("c1", "ls", json.RawMessage(`{}`))},
			StopReason: StopReasonToolUse,
			Timestamp:  2,
		},
		ToolResultMessage{RoleField: RoleToolResult, ToolCallID: "c1", ToolName: "ls", Content: ContentList{NewTextContent("file.go")}, Timestamp: 3},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MessageList
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 messages, got %d", len(out))
	}
	if out[0].Role() != RoleUser {
		t.Errorf("msg 0: want user, got %s", out[0].Role())
	}
	am, ok := out[1].(AssistantMessage)
	if !ok {
		t.Fatalf("msg 1: want AssistantMessage, got %T", out[1])
	}
	if calls := am.ToolCalls(); len(calls) != 1 || calls[0].Name != "ls" {
		t.Errorf("assistant ToolCalls wrong: %+v", calls)
	}
	if out[2].Role() != RoleToolResult {
		t.Errorf("msg 2: want toolResult, got %s", out[2].Role())
	}
}

func TestMessageUnknownRoleRejected(t *testing.T) {
	var out MessageList
	if err := json.Unmarshal([]byte(`[{"role":"system"}]`), &out); err == nil {
		t.Fatal("expected error for unknown role")
	}
}

// TestAgentEventCoverage asserts all 10 event types report a distinct,
// non-empty discriminant (PRD FR-24).
func TestAgentEventCoverage(t *testing.T) {
	events := []AgentEvent{
		AgentStartEvent{}, AgentEndEvent{}, TurnStartEvent{}, TurnEndEvent{},
		MessageStartEvent{}, MessageUpdateEvent{}, MessageEndEvent{},
		ToolExecutionStartEvent{}, ToolExecutionUpdateEvent{}, ToolExecutionEndEvent{},
	}
	seen := map[string]bool{}
	for _, e := range events {
		et := e.EventType()
		if et == "" {
			t.Errorf("%T has empty EventType", e)
		}
		if seen[et] {
			t.Errorf("duplicate event type %q", et)
		}
		seen[et] = true
	}
	if len(seen) != 10 {
		t.Fatalf("want 10 distinct event types, got %d", len(seen))
	}
}

// Server-executed calls must be distinguishable from local ones, and sessions
// persisted before the Kind marker must decode as local (zero value).
func TestToolCallServerKind(t *testing.T) {
	local := NewToolCallContent("c1", "read", json.RawMessage(`{}`))
	if local.IsServer() {
		t.Error("fresh call should not be server")
	}
	srv := NewServerToolCallContent("ws1", "web_search", json.RawMessage(`{"query":"x"}`))
	if !srv.IsServer() {
		t.Error("server ctor should mark server")
	}
	raw, _ := json.Marshal(ToolCallContent{Type: ContentTypeToolCall, ID: "c2", Name: "edit"})
	dec, err := decodeContent(raw)
	if err != nil {
		t.Fatalf("decodeContent error: %v", err)
	}
	if tc, ok := dec.(ToolCallContent); !ok || tc.IsServer() {
		t.Errorf("legacy persisted call should decode as local, got %+v", dec)
	}
}
