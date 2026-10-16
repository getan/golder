// Tests for reasoning/thinking wiring across both wire protocols (issues
// #240-#244): request encoders forwarding ThinkingLevel, the Anthropic
// thinking-block echo on multi-turn tool-use messages, OpenAI reasoning_content
// stream decoding, the max_tokens fallback, and strict-gateway empty-content
// handling.
package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// decodeBody unmarshals an encoded request body into a generic map for asserts.
func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

// TestOpenAIReasoningEffortEncoding verifies ThinkingLevel maps to
// reasoning_effort, and that off/unset omits the field entirely (#240).
func TestOpenAIReasoningEffortEncoding(t *testing.T) {
	base := CompletionRequest{Model: "o3-mini", Context: LlmContext{}}

	// off / unset → no reasoning_effort.
	for _, lvl := range []agentcore.ThinkingLevel{"", agentcore.ThinkingOff} {
		base.Config.ThinkingLevel = lvl
		b, err := encodeOpenAIRequest("", base)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, ok := decodeBody(t, b)["reasoning_effort"]; ok {
			t.Errorf("level %q: reasoning_effort should be omitted", lvl)
		}
	}

	cases := map[agentcore.ThinkingLevel]string{
		// The unified table (reasoning.go) collapses minimal onto the shared
		// low/medium/high ladder, so minimal clamps up to low.
		agentcore.ThinkingMinimal: "low",
		agentcore.ThinkingLow:     "low",
		agentcore.ThinkingMedium:  "medium",
		agentcore.ThinkingHigh:    "high",
		agentcore.ThinkingXHigh:   "high",
		agentcore.ThinkingMax:     "high",
	}
	for lvl, want := range cases {
		base.Config.ThinkingLevel = lvl
		b, err := encodeOpenAIRequest("", base)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got := decodeBody(t, b)["reasoning_effort"]; got != want {
			t.Errorf("level %q: reasoning_effort = %v, want %q", lvl, got, want)
		}
	}
}

// TestAnthropicThinkingEncoding verifies ThinkingLevel enables the thinking
// block with a budget, and off/unset omits it (#240).
func TestAnthropicThinkingEncoding(t *testing.T) {
	base := CompletionRequest{Model: "claude-x", Context: LlmContext{}}

	base.Config.ThinkingLevel = agentcore.ThinkingOff
	b, _ := encodeAnthropicRequest("", base, nil)
	if _, ok := decodeBody(t, b)["thinking"]; ok {
		t.Error("off: thinking block should be omitted")
	}

	base.Config.ThinkingLevel = agentcore.ThinkingMedium
	b, _ = encodeAnthropicRequest("", base, nil)
	th, ok := decodeBody(t, b)["thinking"].(map[string]any)
	if !ok {
		t.Fatal("medium: thinking block missing")
	}
	if th["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", th["type"])
	}
	if bt, _ := th["budget_tokens"].(float64); bt <= 0 {
		t.Errorf("thinking.budget_tokens = %v, want > 0", th["budget_tokens"])
	}
}

// TestAnthropicMaxTokensFallback verifies the fallback prefers the model's
// MaxOutputTokens and otherwise uses the coding-friendly 8192 default (#243).
func TestAnthropicMaxTokensFallback(t *testing.T) {
	req := CompletionRequest{Model: "claude-x", Context: LlmContext{}}

	// No model metadata → 8192 default.
	b, _ := encodeAnthropicRequest("", req, nil)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 8192 {
		t.Errorf("default max_tokens = %v, want 8192", got)
	}

	// Model with a declared cap → that cap.
	models := []Model{{ID: "claude-x", MaxOutputTokens: 12000}}
	b, _ = encodeAnthropicRequest("", req, models)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 12000 {
		t.Errorf("model-cap max_tokens = %v, want 12000", got)
	}

	// Explicit Extra hint still wins.
	req.Config.Extra = map[string]any{"max_tokens": 2000}
	b, _ = encodeAnthropicRequest("", req, models)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 2000 {
		t.Errorf("explicit max_tokens = %v, want 2000", got)
	}
}

// TestAnthropicThinkingBlockEcho verifies a multi-turn assistant message with a
// thinking block + tool call re-emits the thinking block (with signature) ahead
// of the tool_use block (#241).
func TestAnthropicThinkingBlockEcho(t *testing.T) {
	think := agentcore.NewThinkingContent("let me reason")
	think.ThinkingSignature = "sig-abc"
	msg := agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			think,
			agentcore.NewToolCallContent("call_1", "read", json.RawMessage(`{"path":"x"}`)),
		},
	}
	entry := encodeAnthropicMessage(msg, true)
	blocks, ok := entry["content"].([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("want 2 content blocks, got %#v", entry["content"])
	}
	if blocks[0]["type"] != "thinking" {
		t.Errorf("first block type = %v, want thinking", blocks[0]["type"])
	}
	if blocks[0]["signature"] != "sig-abc" {
		t.Errorf("thinking signature = %v, want sig-abc", blocks[0]["signature"])
	}
	if blocks[1]["type"] != "tool_use" {
		t.Errorf("second block type = %v, want tool_use", blocks[1]["type"])
	}
}

// TestAnthropicRedactedThinkingEcho verifies redacted thinking round-trips as a
// redacted_thinking block carrying the signature as data (#241).
func TestAnthropicRedactedThinkingEcho(t *testing.T) {
	think := agentcore.ThinkingContent{Type: agentcore.ContentTypeThinking, Redacted: true, ThinkingSignature: "redacted-data"}
	msg := agentcore.AssistantMessage{Content: agentcore.ContentList{think}}
	entry := encodeAnthropicMessage(msg, true)
	blocks := entry["content"].([]map[string]any)
	if blocks[0]["type"] != "redacted_thinking" || blocks[0]["data"] != "redacted-data" {
		t.Errorf("redacted block = %#v", blocks[0])
	}
}

// TestAnthropicDropsForeignThinking is the cross-protocol guard: a thinking
// block with no signature did not come from this wire (the OpenAI chat driver's
// reasoning_content and the Responses reasoning summary carry none), so it is
// dropped instead of being replayed as a signature-less thinking block the
// Messages API would reject — while the text and calls around it still ride.
func TestAnthropicDropsForeignThinking(t *testing.T) {
	msg := agentcore.AssistantMessage{
		Provider: "deepseek",
		Content: agentcore.ContentList{
			agentcore.NewThinkingContent("foreign chain of thought"),
			agentcore.NewTextContent("here is the answer"),
			agentcore.NewToolCallContent("call_1", "read", json.RawMessage(`{"path":"x"}`)),
		},
	}
	entry := encodeAnthropicMessage(msg, true)
	blocks := entry["content"].([]map[string]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v, want text + tool_use only", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[1]["type"] != "tool_use" {
		t.Errorf("blocks = %#v, want text then tool_use", blocks)
	}
}

// TestAnthropicThinkingOffDropsThinkingBlocks: thinking blocks ride the wire
// only when the request enables thinking (the API validates them against that
// setting), so a signed block stays in the stored message — a later thinking-on
// turn can still echo it — but a thinking-off request sends text and calls alone.
func TestAnthropicThinkingOffDropsThinkingBlocks(t *testing.T) {
	think := agentcore.NewThinkingContent("let me reason")
	think.ThinkingSignature = "sig-abc"
	msg := agentcore.AssistantMessage{Content: agentcore.ContentList{think, agentcore.NewTextContent("done")}}

	entry := encodeAnthropicMessage(msg, false)
	blocks := entry["content"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("thinking-off blocks = %#v, want the text block alone", blocks)
	}
	// The stored message is untouched: the same input with thinking on still
	// echoes the block and its signature.
	entry = encodeAnthropicMessage(msg, true)
	blocks = entry["content"].([]map[string]any)
	if len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[0]["signature"] != "sig-abc" {
		t.Errorf("thinking-on blocks = %#v, want the signed thinking block echoed first", blocks)
	}
}

// TestAnthropicThinkingReplayable pins the provenance rule itself.
func TestAnthropicThinkingReplayable(t *testing.T) {
	cases := []struct {
		name string
		in   agentcore.ThinkingContent
		want bool
	}{
		{"signed", agentcore.ThinkingContent{Thinking: "t", ThinkingSignature: "sig"}, true},
		{"unsigned (foreign)", agentcore.ThinkingContent{Thinking: "t"}, false},
		{"signed but empty text", agentcore.ThinkingContent{ThinkingSignature: "sig"}, false},
		{"redacted with data", agentcore.ThinkingContent{Redacted: true, ThinkingSignature: "data"}, true},
		{"redacted without data", agentcore.ThinkingContent{Redacted: true}, false},
	}
	for _, tc := range cases {
		if got := anthropicThinkingReplayable(tc.in); got != tc.want {
			t.Errorf("%s: replayable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAnthropicRequestDropsForeignThinkingOnTheWire drives the whole path: a
// session whose history carries another provider's reasoning (what a /model or
// /provider switch leaves behind) encodes to a Messages body with no thinking
// block in the messages and no leaked reasoning text, while the call and its
// result stay paired. The request's own thinking parameter stays — the drop is
// about history, not about the level this turn asked for.
func TestAnthropicRequestDropsForeignThinkingOnTheWire(t *testing.T) {
	req := CompletionRequest{
		Model: "claude-haiku-5-5",
		Context: LlmContext{
			Messages: agentcore.MessageList{
				agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("read x")}},
				agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Provider: "deepseek", Content: agentcore.ContentList{
					agentcore.NewThinkingContent("foreign chain of thought"),
					agentcore.NewToolCallContent("call_1", "read", json.RawMessage(`{"path":"x"}`)),
				}},
				agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call_1", ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("file body")}},
			},
		},
		Config: StreamConfig{ThinkingLevel: agentcore.ThinkingHigh},
	}
	body, err := encodeAnthropicRequest("", req, nil)
	if err != nil {
		t.Fatalf("encodeAnthropicRequest: %v", err)
	}
	decoded := decodeBody(t, body)
	if _, ok := decoded["thinking"]; !ok {
		t.Fatalf("the request should still ask for thinking: %s", body)
	}
	msgs, ok := decoded["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %T", decoded["messages"])
	}
	types := map[string]int{}
	for _, m := range msgs {
		entry, _ := m.(map[string]any)
		blocks, _ := entry["content"].([]any)
		for _, b := range blocks {
			block, _ := b.(map[string]any)
			if t, _ := block["type"].(string); t != "" {
				types[t]++
			}
		}
	}
	if types["thinking"] != 0 || types["redacted_thinking"] != 0 {
		t.Errorf("foreign reasoning reached the Messages wire: %v in %s", types, body)
	}
	if strings.Contains(string(body), "foreign chain of thought") {
		t.Errorf("foreign reasoning text leaked: %s", body)
	}
	if types["tool_use"] != 1 || types["tool_result"] != 1 {
		t.Errorf("call/result pairing lost: %v in %s", types, body)
	}
}

// responsesItemTypes unmarshals an encoded Responses input array and counts the
// item types it holds, so a test can say what reached the wire. An input message
// carries no "type" claim on the wire (it is identified by its role), so those
// count as "message".
func responsesItemTypes(t *testing.T, items any) map[string]int {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal items: %v (%s)", err, raw)
	}
	types := map[string]int{}
	for _, item := range decoded {
		typ, _ := item["type"].(string)
		if typ == "" {
			if _, isMessage := item["role"]; isMessage {
				typ = "message"
			}
		}
		if typ != "" {
			types[typ]++
		}
	}
	return types
}

// TestResponsesDropsForeignThinking is the reverse of the Anthropic guard: a
// history born on the Messages wire (signed thinking blocks, API "anthropic")
// replayed to the Responses API carries no thinking and no signature. The
// Responses input builder takes text and calls only, and the opaque reasoning
// items it replays are gated to the model that produced them
// (msg.Provider/ResponseModel match) — which a foreign message never satisfies.
func TestResponsesDropsForeignThinking(t *testing.T) {
	think := agentcore.NewThinkingContent("anthropic chain of thought")
	think.ThinkingSignature = "sig-abc"

	// A text-only step replays as text alone: no reasoning item at all.
	textOnly := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		API:       "anthropic",
		Provider:  "anthropic",
		Content:   agentcore.ContentList{think, agentcore.NewTextContent("the answer")},
	}
	items := appendInputItems(nil, textOnly, false, "opencode-go", "gpt-6-luna", true)
	if types := responsesItemTypes(t, items); types["reasoning"] != 0 || types["message"] != 1 {
		t.Errorf("text-only step item types = %v, want one message and no reasoning", types)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "sig-abc") || strings.Contains(string(raw), "anthropic chain of thought") {
		t.Errorf("Messages-born thinking leaked to the Responses wire: %s", raw)
	}
	if !strings.Contains(string(raw), "the answer") {
		t.Errorf("assistant text lost: %s", raw)
	}

	// A tool-call step under thinking gets the placeholder reasoning head the
	// Responses thinking-mode gateways require (never the foreign text), and the
	// call keeps its id so its output still pairs.
	withCall := textOnly
	withCall.Content = agentcore.ContentList{think, agentcore.NewToolCallContent("toolu_1", "read", json.RawMessage(`{"path":"x"}`))}
	items = appendInputItems(nil, withCall, false, "opencode-go", "gpt-6-luna", true)
	types := responsesItemTypes(t, items)
	if types["reasoning"] != 1 || types["function_call"] != 1 {
		t.Errorf("tool-call step item types = %v, want one reasoning head and one function_call", types)
	}
	raw, _ = json.Marshal(items)
	if strings.Contains(string(raw), "sig-abc") || strings.Contains(string(raw), "anthropic chain of thought") {
		t.Errorf("Messages-born thinking leaked beside the placeholder: %s", raw)
	}
	if !strings.Contains(string(raw), "toolu_1") {
		t.Errorf("call id lost: %s", raw)
	}
}

// TestChatCompletionsDropsForeignThinking: the Chat Completions encoder takes
// text and calls only, so a Messages-born thinking block — signature and all —
// never reaches that wire, and the call/result pairing is by id.
func TestChatCompletionsDropsForeignThinking(t *testing.T) {
	think := agentcore.NewThinkingContent("anthropic chain of thought")
	think.ThinkingSignature = "sig-abc"
	msg := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		API:       "anthropic",
		Provider:  "anthropic",
		Content: agentcore.ContentList{
			think,
			agentcore.NewTextContent("the answer"),
			agentcore.NewToolCallContent("toolu_1", "read", json.RawMessage(`{"path":"x"}`)),
		},
	}
	raw, err := json.Marshal(encodeOpenAIMessage(msg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "sig-abc") || strings.Contains(got, "anthropic chain of thought") {
		t.Errorf("Messages-born thinking leaked to the chat wire: %s", got)
	}
	for _, want := range []string{"the answer", "toolu_1", "tool_calls"} {
		if !strings.Contains(got, want) {
			t.Errorf("chat wire missing %q: %s", want, got)
		}
	}
}

// TestOpenAIAssistantContentNullWithToolCalls verifies a tool-call-only
// assistant turn sends content:null (not ""), while a text-only turn keeps its
// text (#244).
func TestOpenAIAssistantContentNullWithToolCalls(t *testing.T) {
	toolOnly := agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewToolCallContent("call_1", "read", json.RawMessage(`{}`)),
		},
	}
	entry := encodeOpenAIMessage(toolOnly)[0]
	if entry["content"] != nil {
		t.Errorf("tool-only content = %#v, want nil", entry["content"])
	}
	if _, ok := entry["tool_calls"]; !ok {
		t.Error("tool_calls missing")
	}

	textOnly := agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("hi")},
	}
	if got := encodeOpenAIMessage(textOnly)[0]["content"]; got != "hi" {
		t.Errorf("text content = %#v, want hi", got)
	}
}

// TestAnthropicEmptyAssistantNoEmptyText verifies an assistant message with no
// usable content does not emit an empty-string text block (#244).
func TestAnthropicEmptyAssistantNoEmptyText(t *testing.T) {
	msg := agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent("")}}
	entry := encodeAnthropicMessage(msg, true)
	blocks := entry["content"].([]map[string]any)
	if len(blocks) != 1 {
		t.Fatalf("want 1 fallback block, got %d", len(blocks))
	}
	if txt, _ := blocks[0]["text"].(string); strings.TrimSpace(txt) == "" && txt == "" {
		t.Errorf("fallback text block is empty string, want non-empty placeholder")
	}
}

// TestAnthropicThinkingRaisesMaxTokens verifies max_tokens is lifted above the
// thinking budget: Anthropic requires budget_tokens < max_tokens, so a low cap
// must be raised to leave headroom for the visible reply (#243).
func TestAnthropicThinkingRaisesMaxTokens(t *testing.T) {
	req := CompletionRequest{Model: "claude-x", Context: LlmContext{}}
	req.Config.ThinkingLevel = agentcore.ThinkingXHigh // budget 32768

	// Default cap (8192) is below the budget → must be raised above it.
	b, _ := encodeAnthropicRequest("", req, nil)
	body := decodeBody(t, b)
	budget := body["thinking"].(map[string]any)["budget_tokens"].(float64)
	maxTok := body["max_tokens"].(float64)
	if maxTok <= budget {
		t.Errorf("max_tokens = %v, want > budget_tokens %v", maxTok, budget)
	}

	// A caller cap already above budget+headroom is left untouched.
	req.Config.Extra = map[string]any{"max_tokens": 100000}
	b, _ = encodeAnthropicRequest("", req, nil)
	if got := decodeBody(t, b)["max_tokens"].(float64); got != 100000 {
		t.Errorf("max_tokens = %v, want caller value 100000", got)
	}
}

// mustEncodeAnthropic encodes a Messages request or fails the test.
func mustEncodeAnthropic(t *testing.T, providerName string, req CompletionRequest) []byte {
	t.Helper()
	b, err := encodeAnthropicRequest(providerName, req, nil)
	if err != nil {
		t.Fatalf("encodeAnthropicRequest: %v", err)
	}
	return b
}

// TestAnthropicEffortShapedThinkingBody: a model whose catalog entry is
// effort-shaped (claude-haiku-5-5: toggle + an effort ladder, no legacy budget)
// takes the adaptive switch plus output_config.effort. The legacy token-budget
// body is what its gateway rejects outright, and a level below the ladder clamps
// up to its first rung ("minimal" is not a value the API accepts there).
func TestAnthropicEffortShapedThinkingBody(t *testing.T) {
	setReasoningCatalogForTest(t, map[string]map[string]reasoningEntry{
		"opencode-go": {"claude-haiku-5-5": {
			Reasoning: true,
			Options:   []string{"toggle", "effort"},
			Levels:    []string{"low", "medium", "high", "xhigh", "max"},
		}},
	})
	req := CompletionRequest{Model: "claude-haiku-5-5", Context: LlmContext{}}
	req.Config.ThinkingLevel = agentcore.ThinkingHigh

	body := decodeBody(t, mustEncodeAnthropic(t, "opencode-go", req))
	th, ok := body["thinking"].(map[string]any)
	if !ok || th["type"] != "adaptive" {
		t.Fatalf("thinking = %#v, want adaptive", body["thinking"])
	}
	if _, legacy := th["budget_tokens"]; legacy {
		t.Errorf("adaptive shape must not carry budget_tokens: %#v", th)
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok || oc["effort"] != "high" {
		t.Errorf("output_config = %#v, want effort high", body["output_config"])
	}
	// There is no budget to fit under max_tokens, so the default cap stays put.
	if got := body["max_tokens"].(float64); got != 8192 {
		t.Errorf("max_tokens = %v, want the 8192 default left alone", got)
	}

	req.Config.ThinkingLevel = agentcore.ThinkingMinimal
	body = decodeBody(t, mustEncodeAnthropic(t, "opencode-go", req))
	if oc := body["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("minimal clamped to %#v, want low", oc["effort"])
	}
}

// TestAnthropicThinkingShapePrecedence: a model that still advertises the legacy
// budget keeps that body (mixed entries like claude-opus-4-6), an unknown model
// keeps it too (no guessing about a catalog gap), and a toggle-only entry takes
// the adaptive switch with no level named.
func TestAnthropicThinkingShapePrecedence(t *testing.T) {
	setReasoningCatalogForTest(t, map[string]map[string]reasoningEntry{
		"gw": {
			"mixed":  {Reasoning: true, Options: []string{"effort", "budget_tokens"}, Levels: []string{"low", "high"}},
			"toggly": {Reasoning: true, Options: []string{"toggle"}},
		},
	})
	req := CompletionRequest{Model: "mixed", Context: LlmContext{}}
	req.Config.ThinkingLevel = agentcore.ThinkingMedium
	body := decodeBody(t, mustEncodeAnthropic(t, "gw", req))
	if th := body["thinking"].(map[string]any); th["type"] != "enabled" {
		t.Errorf("mixed entry thinking = %#v, want the legacy enabled body", th)
	}
	if _, ok := body["output_config"]; ok {
		t.Errorf("legacy body must not carry output_config: %#v", body["output_config"])
	}

	unknown := CompletionRequest{Model: "not-in-catalog", Context: LlmContext{}}
	unknown.Config.ThinkingLevel = agentcore.ThinkingMedium
	body = decodeBody(t, mustEncodeAnthropic(t, "gw", unknown))
	if th := body["thinking"].(map[string]any); th["type"] != "enabled" {
		t.Errorf("unknown model thinking = %#v, want the historical enabled body", th)
	}

	toggle := CompletionRequest{Model: "toggly", Context: LlmContext{}}
	toggle.Config.ThinkingLevel = agentcore.ThinkingHigh
	body = decodeBody(t, mustEncodeAnthropic(t, "gw", toggle))
	if th := body["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Errorf("toggle-only thinking = %#v, want adaptive", th)
	}
	if _, ok := body["output_config"]; ok {
		t.Errorf("toggle-only request must not name a level: %#v", body["output_config"])
	}
}

// TestOpenAIReasoningContentDecoding verifies the decoder accumulates
// reasoning_content into a ThinkingContent block ahead of text (#242).
func TestOpenAIReasoningContentDecoding(t *testing.T) {
	d := NewOpenAIDecoder()
	chunks := []string{
		`{"id":"c1","choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"harder"}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[{"finish_reason":"stop"}]}`,
	}
	var events []StreamEvent
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		events = append(events, evs...)
	}
	done, _ := d.Finish()
	events = append(events, done...)

	var final agentcore.AssistantMessage
	for _, e := range events {
		if de, ok := e.(StreamDoneEvent); ok {
			final = de.Message
		}
	}
	if len(final.Content) != 2 {
		t.Fatalf("want thinking+text, got %d blocks: %#v", len(final.Content), final.Content)
	}
	th, ok := final.Content[0].(agentcore.ThinkingContent)
	if !ok || th.Thinking != "think harder" {
		t.Errorf("block[0] = %#v, want thinking 'think harder'", final.Content[0])
	}
	txt, ok := final.Content[1].(agentcore.TextContent)
	if !ok || txt.Text != "answer" {
		t.Errorf("block[1] = %#v, want text 'answer'", final.Content[1])
	}

	sawThinking := false
	for _, e := range events {
		if _, ok := e.(StreamThinkingEvent); ok {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Error("expected at least one StreamThinkingEvent")
	}
}
