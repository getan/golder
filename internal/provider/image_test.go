package provider

// Tests for image (multimodal) input encoding on both provider wires (US-010,
// #126). They exercise encodeOpenAIMessage / encodeAnthropicMessage directly
// (unit-level, no HTTP) to assert the exact wire shape of image blocks, plus the
// checkImageSupport guard that turns image input on a text-only model into a
// clear error rather than a silent drop.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// imageUserMessage builds a user message carrying one text block and one image
// block, the common multimodal input shape.
func imageUserMessage(text, data, mime string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{
			agentcore.NewTextContent(text),
			agentcore.NewImageContent(data, mime),
		},
	}
}

// TestEncodeOpenAIMessageImageBlock asserts a user message with an image encodes
// to the multimodal array form with an image_url data URI.
func TestEncodeOpenAIMessageImageBlock(t *testing.T) {
	msg := imageUserMessage("what is this?", "QUJD", "image/png")
	out := encodeOpenAIMessage(msg)
	if len(out) != 1 {
		t.Fatalf("encodeOpenAIMessage returned %d entries, want 1", len(out))
	}
	entry := out[0]
	if entry["role"] != "user" {
		t.Errorf("role = %v, want user", entry["role"])
	}
	parts, ok := entry["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content is %T, want []map[string]any (array form)", entry["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("content has %d parts, want 2 (text + image)", len(parts))
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "what is this?" {
		t.Errorf("text part = %#v", parts[0])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("image part type = %v, want image_url", parts[1]["type"])
	}
	iu, ok := parts[1]["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("image_url is %T", parts[1]["image_url"])
	}
	if want := "data:image/png;base64,QUJD"; iu["url"] != want {
		t.Errorf("image_url.url = %v, want %v", iu["url"], want)
	}
}

// TestEncodeOpenAIMessageNoImageIsString asserts a text-only user message stays
// a plain string (not the array form), preserving the common-case wire shape.
func TestEncodeOpenAIMessageNoImageIsString(t *testing.T) {
	msg := agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("hello")},
	}
	out := encodeOpenAIMessage(msg)
	if s, ok := out[0]["content"].(string); !ok || s != "hello" {
		t.Errorf("content = %#v, want string \"hello\"", out[0]["content"])
	}
}

// TestEncodeAnthropicMessageImageBlock asserts a user message with an image
// encodes to the content-block array form with a base64 image source.
func TestEncodeAnthropicMessageImageBlock(t *testing.T) {
	msg := imageUserMessage("describe", "REVG", "image/jpeg")
	entry := encodeAnthropicMessage(msg)
	if entry["role"] != "user" {
		t.Errorf("role = %v, want user", entry["role"])
	}
	blocks, ok := entry["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content is %T, want []map[string]any (array form)", entry["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("content has %d blocks, want 2 (text + image)", len(blocks))
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "describe" {
		t.Errorf("text block = %#v", blocks[0])
	}
	if blocks[1]["type"] != "image" {
		t.Fatalf("image block type = %v, want image", blocks[1]["type"])
	}
	src, ok := blocks[1]["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is %T", blocks[1]["source"])
	}
	if src["type"] != "base64" || src["media_type"] != "image/jpeg" || src["data"] != "REVG" {
		t.Errorf("source = %#v", src)
	}
}

// TestEncodeAnthropicMessageNoImageIsString asserts a text-only user message
// stays a plain string on the Anthropic wire.
func TestEncodeAnthropicMessageNoImageIsString(t *testing.T) {
	msg := agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("hi")},
	}
	entry := encodeAnthropicMessage(msg)
	if s, ok := entry["content"].(string); !ok || s != "hi" {
		t.Errorf("content = %#v, want string \"hi\"", entry["content"])
	}
}

// TestImageBlocksAreJSONSerializable guards against map value types that
// json.Marshal cannot encode: both wire shapes must round-trip to JSON.
func TestImageBlocksAreJSONSerializable(t *testing.T) {
	msg := imageUserMessage("x", "QQ==", "image/webp")
	if _, err := json.Marshal(encodeOpenAIMessage(msg)); err != nil {
		t.Errorf("marshal openai image message: %v", err)
	}
	if _, err := json.Marshal(encodeAnthropicMessage(msg)); err != nil {
		t.Errorf("marshal anthropic image message: %v", err)
	}
}

// imageToolResult builds a tool result carrying a text summary plus one image,
// the shape view_image returns.
func imageToolResult(text, data, mime string) agentcore.ToolResultMessage {
	return agentcore.ToolResultMessage{
		RoleField:  agentcore.RoleToolResult,
		ToolCallID: "call_1",
		ToolName:   "view_image",
		Content: agentcore.ContentList{
			agentcore.NewTextContent(text),
			agentcore.NewImageContent(data, mime),
		},
	}
}

// TestEncodeOpenAIMessageToolResultImageDegrades asserts Chat Completions (no
// image slot on a role:"tool" message) replaces the image with a placeholder
// instead of dropping it silently.
func TestEncodeOpenAIMessageToolResultImageDegrades(t *testing.T) {
	out := encodeOpenAIMessage(imageToolResult("Viewed image", "QUJD", "image/png"))
	if len(out) != 1 || out[0]["role"] != "tool" {
		t.Fatalf("entries = %#v, want one role:tool entry", out)
	}
	text, _ := out[0]["content"].(string)
	if !strings.Contains(text, "Viewed image") || !strings.Contains(text, "image content omitted") {
		t.Fatalf("content = %q, want text plus an omission placeholder", text)
	}
}

// TestEncodeAnthropicToolResultImage asserts the Messages API tool_result
// carries the image as a content block.
func TestEncodeAnthropicToolResultImage(t *testing.T) {
	entry := encodeAnthropicMessage(imageToolResult("Viewed image", "REVG", "image/jpeg"))
	blocks, ok := entry["content"].([]map[string]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v, want one tool_result block", entry["content"])
	}
	toolResult := blocks[0]
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_1" {
		t.Fatalf("tool_result = %#v", toolResult)
	}
	contentBlocks, ok := toolResult["content"].([]map[string]any)
	if !ok || len(contentBlocks) != 2 {
		t.Fatalf("tool_result content = %#v, want text + image blocks", toolResult["content"])
	}
	if contentBlocks[0]["type"] != "text" || contentBlocks[0]["text"] != "Viewed image" {
		t.Errorf("text block = %#v", contentBlocks[0])
	}
	if contentBlocks[1]["type"] != "image" {
		t.Fatalf("image block = %#v", contentBlocks[1])
	}
	src, _ := contentBlocks[1]["source"].(map[string]any)
	if src["media_type"] != "image/jpeg" || src["data"] != "REVG" {
		t.Errorf("source = %#v", src)
	}
}

// TestAnthropicToolResultTextOnlyStaysString keeps the common case unchanged.
func TestAnthropicToolResultTextOnlyStaysString(t *testing.T) {
	entry := encodeAnthropicMessage(agentcore.ToolResultMessage{
		RoleField: agentcore.RoleToolResult, ToolCallID: "call_1", ToolName: "bash",
		Content: agentcore.ContentList{agentcore.NewTextContent("ok")},
	})
	blocks := entry["content"].([]map[string]any)
	if s, ok := blocks[0]["content"].(string); !ok || s != "ok" {
		t.Fatalf("tool_result content = %#v, want string \"ok\"", blocks[0]["content"])
	}
}

// TestResponsesToolResultImageItem asserts the Responses function_call_output
// carries content items (input_text + input_image data URI) for an image
// result, and stays a plain string otherwise.
func TestResponsesToolResultImageItem(t *testing.T) {
	raw, err := json.Marshal(toolResultFunctionCallOutput(imageToolResult("Viewed image", "QUJD", "image/png")))
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	var item struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("unmarshal item %s: %v", raw, err)
	}
	if item.Type != "function_call_output" || item.CallID != "call_1" {
		t.Fatalf("item = %s", raw)
	}
	if len(item.Output) != 2 {
		t.Fatalf("output = %s, want text + image items", raw)
	}
	if item.Output[0].Type != "input_text" || item.Output[0].Text != "Viewed image" {
		t.Errorf("text item = %#v", item.Output[0])
	}
	if item.Output[1].Type != "input_image" || item.Output[1].ImageURL != "data:image/png;base64,QUJD" {
		t.Errorf("image item = %#v", item.Output[1])
	}

	textRaw, err := json.Marshal(toolResultFunctionCallOutput(agentcore.ToolResultMessage{
		RoleField: agentcore.RoleToolResult, ToolCallID: "call_1", ToolName: "bash",
		Content: agentcore.ContentList{agentcore.NewTextContent("ok")},
	}))
	if err != nil {
		t.Fatalf("marshal text item: %v", err)
	}
	if !strings.Contains(string(textRaw), `"output":"ok"`) {
		t.Errorf("text-only output = %s, want the plain string form", textRaw)
	}
}

// TestCheckImageSupport asserts the capability guard: image input on a model
// that declares SupportsImages passes; on a text-only model it errors; and a
// text-only prompt always passes regardless of the model.
func TestCheckImageSupport(t *testing.T) {
	models := []Model{
		{ID: "vision-1", SupportsImages: true},
		{ID: "text-1", SupportsImages: false},
	}
	imgMsgs := []agentcore.Message{imageUserMessage("q", "QQ==", "image/png")}
	textMsgs := []agentcore.Message{agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("no image")},
	}}

	if err := checkImageSupport("p", "vision-1", models, imgMsgs); err != nil {
		t.Errorf("vision model rejected image input: %v", err)
	}
	if err := checkImageSupport("p", "text-1", models, imgMsgs); err == nil {
		t.Error("text-only model accepted image input, want error")
	}
	if err := checkImageSupport("p", "text-1", models, textMsgs); err != nil {
		t.Errorf("text-only prompt on text model errored: %v", err)
	}
	// Unknown model (not in catalog) defers to provider validation → no error.
	if err := checkImageSupport("p", "unknown", models, imgMsgs); err != nil {
		t.Errorf("unknown model errored on image input: %v", err)
	}
}
