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
				agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, API: "gemini", Content: agentcore.ContentList{
					withSignature(agentcore.NewToolCallContent("call-1", "ls", json.RawMessage(`{"path":"."}`)), "sig-1"),
					withSignature(agentcore.NewToolCallContent("call-2", "stat", json.RawMessage(`{"path":"a.go"}`)), "sig-2"),
				}},
				agentcore.ToolResultMessage{ToolCallID: "call-1", Content: agentcore.ContentList{
					agentcore.NewTextContent("a.go"),
				}},
				agentcore.ToolResultMessage{ToolCallID: "call-2", Content: agentcore.ContentList{
					agentcore.NewTextContent("regular file"),
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
	if len(parts) != 2 {
		t.Fatalf("assistant turn parts = %d, want two functionCall parts", len(parts))
	}
	call := parts[0].(map[string]any)["functionCall"].(map[string]any)
	if parts[0].(map[string]any)["thoughtSignature"] != "sig-1" {
		t.Errorf("part signature = %v, want sig-1 at the part level", parts[0])
	}
	if _, nested := call["thoughtSignature"]; nested {
		t.Errorf("thoughtSignature must not be nested inside functionCall: %v", call)
	}
	result := contents[2].(map[string]any)
	respParts := result["parts"].([]any)
	if len(respParts) != 2 {
		t.Fatalf("tool turn parts = %d, want both responses in one user turn", len(respParts))
	}
	resp := respParts[0].(map[string]any)["functionResponse"].(map[string]any)
	if resp["name"] != "ls" {
		t.Errorf("functionResponse name = %v, want ls", resp["name"])
	}
	if gen, ok := body["generationConfig"].(map[string]any); !ok || gen["maxOutputTokens"] == nil {
		t.Errorf("generationConfig = %v, want a maxOutputTokens", body["generationConfig"])
	}
}

func withSignature(c agentcore.ToolCallContent, sig string) agentcore.ToolCallContent {
	c.ThoughtSignature = sig
	return c
}

// A history carried over from another provider has function calls without a
// Gemini thought signature. Replaying them as functionCall parts fails the
// v1beta validation, so they and their results become text; a Gemini-origin
// turn keeps the native parts.
func TestEncodeGeminiRequestForeignToolHistory(t *testing.T) {
	req := CompletionRequest{
		Model: "gemini-4-argon",
		Context: LlmContext{
			Messages: agentcore.MessageList{
				agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{
					agentcore.NewTextContent("list files"),
				}},
				agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, API: "openai", Content: agentcore.ContentList{
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
	body := string(raw)
	for _, unwanted := range []string{"functionCall", "functionResponse"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("foreign history emitted %s: %s", unwanted, body)
		}
	}
	for _, want := range []string{"Tool call ls", "Tool result (ls)"} {
		if !strings.Contains(body, want) {
			t.Errorf("foreign history lost %q: %s", want, body)
		}
	}
}

// TestGeminiDecoder pins the stream conversion: thought parts land in a thinking
// block, text in a text block, functionCall becomes a tool call, usage and the
// stop reason surface in the terminal message.
func TestGeminiDecoder(t *testing.T) {
	d := NewGeminiDecoder()
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"plan","thought":true},{"text":"hello"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"ls","args":{"path":"."}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"cachedContentTokenCount":2}}`,
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
	if tc, ok := msg.Content[2].(agentcore.ToolCallContent); !ok || tc.ThoughtSignature != "sig-1" {
		t.Errorf("tool call signature = %+v, want sig-1", msg.Content[2])
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
	env := func(string) string { return "" }
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
	const custom = "https://custom.example/v1beta"
	p, _, err = ResolveNamedProvider("opencode-go", "gemini-4-argon-contributor", custom, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := UnwrapProvider(p).(*geminiCompatDriver); !ok || d.baseURL != custom {
		t.Fatal("Gemini ignored the provider's explicit Base URL")
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
