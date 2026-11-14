// This file implements the Gemini v1beta wire: POST
// {base}/models/{model}:streamGenerateContent?alt=sse, the third driver beside
// the OpenAI-family and Anthropic-Messages ones. It is scoped to what the agent
// loop uses — text, thought parts, function calls, usage and the terminal stop
// reason — and reports images as unsupported through the model metadata until
// the inlineData shape is added.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// geminiDefaultBaseURL is the public Gemini API. Gateways pass their own
// endpoint (opencode-go serves it at …/google/v1beta).
const geminiDefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// geminiCompatDriver is the driver for the Gemini v1beta wire.
type geminiCompatDriver struct {
	name       string
	baseURL    string
	models     []Model
	authHeader func(req *http.Request, apiKey string)
}

// NewGeminiProtocolProvider builds a named Gemini-v1beta provider. name is the
// provider's own name (so errors reference it), baseURL the already-resolved
// endpoint, and authScheme picks the header: x-goog-api-key for the first-party
// API, Bearer for gateways that authenticate their Google wire like the rest.
func NewGeminiProtocolProvider(name, baseURL, authScheme string, models []Model) Provider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = geminiDefaultBaseURL
	}
	return &geminiCompatDriver{
		name:       name,
		baseURL:    strings.TrimRight(baseURL, "/"),
		models:     models,
		authHeader: geminiAuthHeaderFor(authScheme),
	}
}

func geminiAuthHeaderFor(scheme string) func(*http.Request, string) {
	if strings.EqualFold(strings.TrimSpace(scheme), AuthBearer) {
		return func(req *http.Request, key string) {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	return func(req *http.Request, key string) {
		req.Header.Set("x-goog-api-key", key)
	}
}

func (d *geminiCompatDriver) Name() string    { return d.name }
func (d *geminiCompatDriver) Models() []Model { return d.models }

// StreamCompletion builds the v1beta request and streams it, decoding with
// GeminiDecoder.
func (d *geminiCompatDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if strings.TrimSpace(req.Config.APIKey) == "" {
		return nil, fmt.Errorf("%s: missing API key (set %s)", d.name, APIKeyEnvHint(d.name))
	}
	if err := checkImageSupport(d.name, req.Model, d.models, req.Context.Messages); err != nil {
		return nil, err
	}
	body, err := encodeGeminiRequest(req)
	if err != nil {
		return nil, fmt.Errorf("%s: build request body: %w", d.name, err)
	}
	path := "/models/" + url.PathEscape(req.Model) + ":streamGenerateContent?alt=sse"
	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if d.authHeader != nil {
			d.authHeader(httpReq, req.Config.APIKey)
		}
		if sid := SessionHeaderValue(d.name, req.Config.Extra); sid != "" {
			httpReq.Header.Set(OpencodeSessionHeader, sid)
		}
		return httpReq, nil
	}
	return StreamRequest(ctx, TransportConfig{
		Client:     clientForURL(d.name, d.baseURL),
		NewRequest: newReq,
		Decoder:    NewGeminiDecoder(),
	})
}

// encodeGeminiRequest serializes a CompletionRequest into the v1beta shape:
// contents (user/model turns), systemInstruction, functionDeclarations and a
// generationConfig carrying the output cap. Tool results become
// functionResponse parts; the function's name comes from the assistant message
// that requested it, because v1beta identifies the response by name, not id.
func encodeGeminiRequest(req CompletionRequest) ([]byte, error) {
	toolNames := map[string]string{}
	for _, m := range req.Context.Messages {
		am, ok := m.(agentcore.AssistantMessage)
		if !ok {
			continue
		}
		for _, c := range am.Content {
			if tc, ok := c.(agentcore.ToolCallContent); ok && tc.ID != "" {
				toolNames[tc.ID] = tc.Name
			}
		}
	}
	contents := make([]map[string]any, 0, len(req.Context.Messages))
	nativeCalls := map[string]bool{}
	for i := 0; i < len(req.Context.Messages); i++ {
		switch mm := req.Context.Messages[i].(type) {
		case agentcore.UserMessage:
			contents = append(contents, map[string]any{"role": "user", "parts": geminiUserParts(mm.Content)})
		case agentcore.AssistantMessage:
			parts, native := geminiModelParts(mm.Content, mm.API == "gemini")
			for id := range native {
				nativeCalls[id] = true
			}
			contents = append(contents, map[string]any{"role": "model", "parts": parts})
		case agentcore.ToolResultMessage:
			// v1beta pairs function responses with a model turn's function
			// calls per content, not per message: parallel calls answered in
			// separate user turns are rejected as a count mismatch.
			parts := []map[string]any{geminiToolResult(mm, toolNames, nativeCalls)}
			for i+1 < len(req.Context.Messages) {
				next, ok := req.Context.Messages[i+1].(agentcore.ToolResultMessage)
				if !ok {
					break
				}
				i++
				parts = append(parts, geminiToolResult(next, toolNames, nativeCalls))
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
	}
	body := map[string]any{"contents": contents}
	if sp := strings.TrimSpace(req.Context.SystemPrompt); sp != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": sp}}}
	}
	if tools := encodeGeminiTools(req.Context.Tools); len(tools) > 0 {
		body["tools"] = []map[string]any{{"functionDeclarations": tools}}
	}
	maxTok := maxOutputTokensFor(req)
	if maxTok <= 0 {
		maxTok = 8192
	}
	body["generationConfig"] = map[string]any{"maxOutputTokens": maxTok}
	return json.Marshal(body)
}

// geminiUserParts encodes the user-visible blocks of one message.
func geminiUserParts(list agentcore.ContentList) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		if tc, ok := c.(agentcore.TextContent); ok {
			out = append(out, map[string]any{"text": tc.Text})
		}
	}
	return out
}

// geminiModelParts encodes an assistant turn: text, thoughts (flagged, so the
// model knows they are reasoning rather than reply) and function calls. Only a
// Gemini-origin turn may carry functionCall parts: v1beta validates the thought
// signature on them, and a call another protocol produced has none. Foreign
// calls are written as text instead, so a /model switch keeps their meaning
// without asking Gemini to accept a call it never issued.
func geminiModelParts(list agentcore.ContentList, fromGemini bool) ([]map[string]any, map[string]bool) {
	out := make([]map[string]any, 0, len(list))
	native := map[string]bool{}
	for _, c := range list {
		switch cc := c.(type) {
		case agentcore.TextContent:
			out = append(out, map[string]any{"text": cc.Text})
		case agentcore.ThinkingContent:
			if cc.Thinking != "" {
				out = append(out, map[string]any{"text": cc.Thinking, "thought": true})
			}
		case agentcore.ToolCallContent:
			if cc.IsServer() {
				continue
			}
			if !fromGemini {
				out = append(out, map[string]any{"text": foreignCallText(cc)})
				continue
			}
			native[cc.ID] = true
			args := map[string]any{}
			if len(cc.Arguments) > 0 {
				_ = json.Unmarshal(cc.Arguments, &args)
			}
			part := map[string]any{"functionCall": map[string]any{
				"name": cc.Name, "args": args,
			}}
			// Gemini 3 requires the signature it issued with a function call to
			// be replayed on that call; dropping it fails the next tool turn.
			// It is a sibling of functionCall at the part level, not a field
			// inside the call (the API rejects the nested form).
			if cc.ThoughtSignature != "" {
				part["thoughtSignature"] = cc.ThoughtSignature
			}
			out = append(out, part)
		}
	}
	return out, native
}

// geminiToolResult encodes one local tool result. v1beta identifies a
// functionResponse by the name from the assistant turn that requested it, not
// by id; a result whose call did not go over this wire is kept as text.
func geminiToolResult(m agentcore.ToolResultMessage, toolNames map[string]string, native map[string]bool) map[string]any {
	var text strings.Builder
	for _, c := range m.Content {
		if tc, ok := c.(agentcore.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if !native[m.ToolCallID] {
		name := toolNames[m.ToolCallID]
		if name == "" {
			name = m.ToolCallID
		}
		return map[string]any{"text": fmt.Sprintf("Tool result (%s): %s", name, text.String())}
	}
	return map[string]any{"functionResponse": map[string]any{
		"name":     toolNames[m.ToolCallID],
		"response": map[string]any{"content": text.String()},
	}}
}

// foreignCallText is the transcript form of a call another protocol issued.
func foreignCallText(c agentcore.ToolCallContent) string {
	args := strings.TrimSpace(string(c.Arguments))
	if args == "" {
		args = "{}"
	}
	return fmt.Sprintf("Tool call %s %s", c.Name, args)
}

// geminiSchemaKeys are the JSON Schema keywords v1beta accepts in a function
// declaration. Anything else — most commonly additionalProperties, which
// golder's tool schemas carry for OpenAI — makes the gateway answer 400 with an
// empty body, so the sanitizer keeps only this subset.
var geminiSchemaKeys = map[string]bool{
	"type": true, "properties": true, "required": true, "items": true,
	"enum": true, "description": true, "format": true, "nullable": true,
	"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
	"minLength": true, "maxLength": true, "pattern": true,
}

// geminiSchema cleans one tool schema into the subset v1beta accepts.
func geminiSchema(raw json.RawMessage) any {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return map[string]any{"type": "object"}
	}
	return sanitizeGeminiSchema(v)
}

func sanitizeGeminiSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch k {
			case "properties":
				props, ok := val.(map[string]any)
				if !ok {
					continue
				}
				clean := make(map[string]any, len(props))
				for name, sub := range props {
					clean[name] = sanitizeGeminiSchema(sub)
				}
				out[k] = clean
			case "items":
				out[k] = sanitizeGeminiSchema(val)
			default:
				if geminiSchemaKeys[k] {
					out[k] = val
				}
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, sub := range x {
			out = append(out, sanitizeGeminiSchema(sub))
		}
		return out
	default:
		return v
	}
}

// encodeGeminiTools maps golder tools onto functionDeclarations.
func encodeGeminiTools(tools []agentcore.AgentTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"parameters":  geminiSchema(json.RawMessage(t.Schema())),
		})
	}
	return out
}

// GeminiDecoder turns v1beta SSE chunks into StreamEvents. Each chunk carries
// complete parts (v1beta does not stream inside a part), so the decoder appends
// text/thought and realizes function calls as they arrive.
type GeminiDecoder struct {
	text      strings.Builder
	thinking  strings.Builder
	toolCalls []agentcore.ToolCallContent
	usage     *agentcore.Usage
	stop      string
	callSeq   int
	done      bool
}

func NewGeminiDecoder() *GeminiDecoder { return &GeminiDecoder{} }

type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             string `json:"text"`
				Thought          bool   `json:"thought"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

// Decode turns one v1beta SSE payload into events.
func (d *GeminiDecoder) Decode(payload []byte) ([]StreamEvent, error) {
	var ch geminiChunk
	if err := json.Unmarshal(payload, &ch); err != nil {
		return nil, fmt.Errorf("gemini: parse chunk: %w", err)
	}
	var out []StreamEvent
	for _, cand := range ch.Candidates {
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				d.callSeq++
				args := p.FunctionCall.Args
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				call := agentcore.NewToolCallContent(
					fmt.Sprintf("gemini-call-%d", d.callSeq), p.FunctionCall.Name, args)
				call.ThoughtSignature = p.ThoughtSignature
				d.toolCalls = append(d.toolCalls, call)
				out = append(out, StreamToolCallEvent{Partial: d.partial()})
			case p.Thought:
				d.thinking.WriteString(p.Text)
				out = append(out, StreamThinkingEvent{Partial: d.partial()})
			case p.Text != "":
				d.text.WriteString(p.Text)
				out = append(out, StreamTextEvent{Partial: d.partial()})
			}
		}
		if cand.FinishReason != "" {
			d.stop = mapGeminiStopReason(cand.FinishReason)
		}
	}
	if ch.UsageMetadata != nil {
		d.usage = &agentcore.Usage{
			InputTokens:     ch.UsageMetadata.PromptTokenCount,
			OutputTokens:    ch.UsageMetadata.CandidatesTokenCount,
			CacheReadTokens: ch.UsageMetadata.CachedContentTokenCount,
		}
	}
	return out, nil
}

// Finish emits the terminal done event (idempotent).
func (d *GeminiDecoder) Finish() ([]StreamEvent, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	msg := d.partial()
	if msg.StopReason == "" {
		msg.StopReason = agentcore.StopReasonEndTurn
	}
	return []StreamEvent{StreamDoneEvent{Message: msg}}, nil
}

// Terminal reports whether a candidate carried finishReason. A transport error
// after that point lost HTTP framing, not answer content.
func (d *GeminiDecoder) Terminal() bool { return d.done || d.stop != "" }

// partial materializes the accumulated state.
func (d *GeminiDecoder) partial() agentcore.AssistantMessage {
	msg := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		API:        "gemini",
		Provider:   "gemini",
		StopReason: d.stop,
		Usage:      d.usage,
	}
	if d.thinking.Len() > 0 {
		msg.Content = append(msg.Content, agentcore.NewThinkingContent(d.thinking.String()))
	}
	if d.text.Len() > 0 {
		msg.Content = append(msg.Content, agentcore.NewTextContent(d.text.String()))
	}
	for _, tc := range d.toolCalls {
		msg.Content = append(msg.Content, tc)
	}
	return msg
}

// mapGeminiStopReason maps v1beta finishReason strings onto golder stop reasons.
func mapGeminiStopReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "MAX_TOKENS":
		return agentcore.StopReasonLength
	case "STOP":
		return agentcore.StopReasonEndTurn
	default:
		return agentcore.StopReasonEndTurn
	}
}
