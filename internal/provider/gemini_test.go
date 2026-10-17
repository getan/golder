package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// TestGeminiSchemaStripsUnsupportedKeywords: golder's schemas carry
// additionalProperties (OpenAI-shaped) and the gateway answers 400 for it, so
// only the v1beta subset survives — at every level.
func TestGeminiSchemaStripsUnsupportedKeywords(t *testing.T) {
	raw := `{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":false,"required":["command"],"properties":{"command":{"type":"string","description":"cmd"},"nested":{"type":"object","additionalProperties":false,"properties":{"x":{"type":"string"}}}}}`
	var out map[string]any
	b, err := json.Marshal(geminiSchema(json.RawMessage(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["additionalProperties"]; ok {
		t.Error("additionalProperties survived")
	}
	if _, ok := out["$schema"]; ok {
		t.Error("$schema survived")
	}
	if out["type"] != "object" {
		t.Errorf("type = %v, want object", out["type"])
	}
	props := out["properties"].(map[string]any)
	nested := props["nested"].(map[string]any)
	if _, ok := nested["additionalProperties"]; ok {
		t.Error("nested additionalProperties survived")
	}
	if nested["type"] != "object" {
		t.Errorf("nested type = %v, want object", nested["type"])
	}
}

// TestEncodeGeminiRequest pins the v1beta body: contents with user/model turns,
// systemInstruction, functionDeclarations, functionResponse for a tool result
// (by name, from the assistant message that called it), and the output cap.
func TestEncodeGeminiRequest(t *testing.T) {
	req := CompletionRequest{
		Model: "gemini-4-argon",
		Context: LlmContext{
			SystemPrompt: "be brief",
			Messages: agentcore.MessageList{
				agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{
					agentcore.NewTextContent("list files"),
				}},
				agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{
					agentcore.NewToolCallContent("call-1", "ls", json.RawMessage(`{"path":"."}`)),
				}},
				agentcore.ToolResultMessage{ToolCallID: "call-1", Content: agentcore.ContentList{
					agentcore.NewTextContent("a.go"),
				}},
			},
		},
	}
	raw, err := encodeGeminiRequest(req)
	if err != nil {
		t.Fatalf("encodeGeminiRequest: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["systemInstruction"] == nil {
		t.Error("missing systemInstruction")
	}
	contents, ok := body["contents"].([]any)
	if !ok || len(contents) != 3 {
		t.Fatalf("contents = %v, want three turns", body["contents"])
	}
	turn := contents[1].(map[string]any)
	if turn["role"] != "model" {
		t.Errorf("assistant turn role = %v, want model", turn["role"])
	}
	parts := turn["parts"].([]any)
	if _, ok := parts[0].(map[string]any)["functionCall"]; !ok {
		t.Errorf("assistant turn = %v, want a functionCall part", parts[0])
	}
	result := contents[2].(map[string]any)
	respParts := result["parts"].([]any)
	resp := respParts[0].(map[string]any)["functionResponse"].(map[string]any)
	if resp["name"] != "ls" {
		t.Errorf("functionResponse name = %v, want ls", resp["name"])
	}
	if gen, ok := body["generationConfig"].(map[string]any); !ok || gen["maxOutputTokens"] == nil {
		t.Errorf("generationConfig = %v, want a maxOutputTokens", body["generationConfig"])
	}
}

// TestGeminiDecoder pins the stream conversion: thought parts land in a thinking
// block, text in a text block, functionCall becomes a tool call, usage and the
// stop reason surface in the terminal message.
func TestGeminiDecoder(t *testing.T) {
	d := NewGeminiDecoder()
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"plan","thought":true},{"text":"hello"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"ls","args":{"path":"."}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"cachedContentTokenCount":2}}`,
	}
	var events []StreamEvent
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		events = append(events, evs...)
	}
	done, err := d.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	events = append(events, done...)
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4 (thinking, text, tool call, done)", len(events))
	}
	msg := events[len(events)-1].(StreamDoneEvent).Message
	if msg.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("stop = %q, want end_turn", msg.StopReason)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 7 || msg.Usage.OutputTokens != 3 || msg.Usage.CacheReadTokens != 2 {
		t.Errorf("usage = %+v, want 7/3/2", msg.Usage)
	}
	var kinds []string
	for _, c := range msg.Content {
		switch c.(type) {
		case agentcore.ThinkingContent:
			kinds = append(kinds, "thinking")
		case agentcore.TextContent:
			kinds = append(kinds, "text")
		case agentcore.ToolCallContent:
			kinds = append(kinds, "tool")
		}
	}
	if strings.Join(kinds, ",") != "thinking,text,tool" {
		t.Errorf("content kinds = %v, want thinking,text,tool", kinds)
	}
}

// TestGeminiResolve: the registry's Gemini selector builds the v1beta driver on
// the variant's own base URL, under the gateway's name.
func TestGeminiResolve(t *testing.T) {
	env := func(string) string { return "key" }
	p, name, err := ResolveNamedProvider("opencode-go", "gemini-4-argon-contributor", "", "", env)
	if err != nil {
		t.Fatalf("ResolveNamedProvider: %v", err)
	}
	if name != "opencode-go" {
		t.Errorf("name = %q, want opencode-go", name)
	}
	p = UnwrapProvider(p)
	d, ok := p.(*geminiCompatDriver)
	if !ok {
		t.Fatalf("driver = %T, want *geminiCompatDriver", p)
	}
	if !strings.Contains(d.baseURL, "/google/v1beta") {
		t.Errorf("base URL = %q, want the gateway's google/v1beta endpoint", d.baseURL)
	}
}

// TestDecisionModelRefused: jev is a decision model, not a conversation target.
func TestDecisionModelRefused(t *testing.T) {
	if !IsDecisionModel("jev-1.13") || !IsDecisionModel("systemone") || IsDecisionModel("gpt-5") {
		t.Fatal("IsDecisionModel misclassifies")
	}
	if _, _, err := ResolveNamedProvider("opencode-go", "jev-1.13", "", "", func(string) string { return "k" }); err == nil {
		t.Fatal("a decision model must not resolve as a conversation provider")
	}
}
