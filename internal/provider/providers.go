// This file implements the first concrete Providers (US-013): Bedrock,
// OpenRouter, and Ollama. Each satisfies the Provider interface by building an
// *http.Request and delegating to the shared transport (StreamRequest) with a
// provider-appropriate Decoder — no bespoke HTTP/SSE handling per provider.
//
// Two backing driver shapes cover all three:
//
//   - openAICompatDriver — POSTs to {baseURL}/chat/completions in the OpenAI
//     Chat Completions wire format, decoded by OpenAIDecoder. OpenRouter and
//     Ollama are instances of it; it is the generic OpenAI-compatible layer,
//     reusable for any gateway (Groq, together, local servers, …).
//   - anthropicCompatDriver — POSTs the Anthropic Messages wire format, decoded
//     by AnthropicDecoder. Bedrock rides this: Anthropic-on-Bedrock speaks the
//     Messages API, so the decoder is reused wholesale.
//
// Failures follow the dual failure model (FR-13): only the earliest "cannot
// build the stream" case (missing key, bad request construction) is a returned
// error; every runtime failure rides the stream as a terminal error event,
// which StreamRequest already guarantees.
//
// Security (US-012 / US-026): API keys are referenced by provider name in any
// error; secret values are never logged or embedded in error text.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
)

// providerBaseURLs holds the default endpoint per built-in provider. A base URL
// is a transport concern (the decoders are URL-agnostic), so overriding it —
// e.g. pointing Ollama at a remote host — needs no decoder change.
const (
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	ollamaBaseURL     = "http://localhost:11434/v1"
	// nvidiaBaseURL is NVIDIA's hosted NIM endpoint. It speaks the OpenAI
	// Chat Completions wire format, so it rides openAICompatDriver unchanged.
	nvidiaBaseURL = "https://integrate.api.nvidia.com/v1"
	// bedrockBaseURL is a placeholder default; real Bedrock endpoints are
	// region-specific (bedrock-runtime.<region>.amazonaws.com) and supplied at
	// construction. It is exported as a field so callers set the resolved URL.
	bedrockBaseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
	// anthropicBaseURL is the public Anthropic Messages API endpoint, the default
	// for a --protocol=anthropic provider when no --base-url is given.
	anthropicBaseURL = "https://api.anthropic.com/v1"
	// anthropicAPIVersion is the required anthropic-version header value sent with
	// every direct-Anthropic request.
	anthropicAPIVersion = "2023-06-01"
)

// ---------------------------------------------------------------------------
// OpenAI-compatible driver (OpenRouter, Ollama, and any OpenAI-compatible API).
// ---------------------------------------------------------------------------

// openAICompatDriver is the shared backing for every OpenAI-compatible
// provider. It holds the provider identity, endpoint, model catalog, and the
// auth scheme; StreamCompletion builds the chat-completions request and hands
// it to the transport with a fresh OpenAIDecoder.
type openAICompatDriver struct {
	name    string
	baseURL string
	models  []Model
	// requiresAuth reports whether an Authorization: Bearer header is sent.
	// Ollama (local) needs none; OpenRouter does.
	requiresAuth bool
	// extraHeaders are attached to every request (e.g. OpenRouter attribution).
	extraHeaders map[string]string
}

func (d *openAICompatDriver) Name() string    { return d.name }
func (d *openAICompatDriver) Models() []Model { return d.models }

// StreamCompletion builds the OpenAI Chat Completions request and streams it.
func (d *openAICompatDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if d.requiresAuth && strings.TrimSpace(req.Config.APIKey) == "" {
		// Early "cannot build the stream": reference the provider, never a value.
		return nil, fmt.Errorf("%s: missing API key (set %s)", d.name, APIKeyEnvHint(d.name))
	}
	if err := checkImageSupport(d.name, req.Model, d.models, req.Context.Messages); err != nil {
		return nil, err
	}
	body, err := encodeOpenAIRequest(d.name, req)
	if err != nil {
		return nil, fmt.Errorf("%s: build request body: %w", d.name, err)
	}
	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if d.requiresAuth {
			httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)
		}
		for k, v := range d.extraHeaders {
			httpReq.Header.Set(k, v)
		}
		if sid := SessionHeaderValue(d.name, req.Config.Extra); sid != "" {
			httpReq.Header.Set(OpencodeSessionHeader, sid)
		}
		return httpReq, nil
	}
	return StreamRequest(ctx, TransportConfig{Client: clientForURL(d.name, d.baseURL), NewRequest: newReq, Decoder: NewOpenAIDecoder()})
}

// encodeOpenAIRequest serializes a CompletionRequest into an OpenAI Chat
// Completions JSON body with streaming enabled and usage requested.
func encodeOpenAIRequest(providerName string, req CompletionRequest) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Context.Messages)+1)
	if sp := req.Context.SystemPrompt; sp != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sp})
	}
	for _, m := range req.Context.Messages {
		msgs = append(msgs, encodeOpenAIMessage(m)...)
	}
	body := map[string]any{
		"model":          req.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	// Reasoning effort: when a thinking level is requested, forward it as the
	// OpenAI `reasoning_effort` field. Reasoning models (o-series, DeepSeek-R1,
	// GLM-thinking, …) read this to open their reasoning channel; omitting it
	// leaves them at their default and effectively disables extended reasoning.
	if effort := WireReasoningEffort(providerName, req.Model, req.Config.ThinkingLevel); effort != "" {
		body["reasoning_effort"] = effort
	}
	if tools := encodeOpenAITools(req.Context.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

// encodeOpenAIMessage maps one golder message onto the OpenAI wire shape. An
// assistant message may expand to content + tool_calls in a single entry; a
// tool result becomes a role:"tool" entry keyed by tool_call_id.
func encodeOpenAIMessage(m agentcore.Message) []map[string]any {
	switch msg := m.(type) {
	case agentcore.UserMessage:
		return []map[string]any{{"role": "user", "content": openAIUserContent(msg.Content)}}
	case agentcore.CompactionMessage:
		// A compaction checkpoint stands in for compacted history as user text.
		u := msg.AsUserMessage()
		return []map[string]any{{"role": "user", "content": openAIUserContent(u.Content)}}
	case agentcore.AssistantMessage:
		entry := map[string]any{"role": "assistant"}
		text := agentcore.ContentToText(msg.Content)
		var toolCalls []map[string]any
		for _, c := range msg.Content {
			if tc, ok := c.(agentcore.ToolCallContent); ok {
				// Provider-executed calls never ride the Chat Completions
				// function wire: there is no output to pair them with.
				if tc.IsServer() {
					continue
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": string(tc.Arguments),
					},
				})
			}
		}
		if len(toolCalls) > 0 {
			entry["tool_calls"] = toolCalls
			// With tool calls present, send content as JSON null when there is no
			// accompanying text: an empty string trips strict gateways (e.g. vLLM)
			// that expect null | non-empty for an assistant tool-call turn.
			if text == "" {
				entry["content"] = nil
			} else {
				entry["content"] = text
			}
		} else {
			entry["content"] = text
		}
		return []map[string]any{entry}
	case agentcore.ToolResultMessage:
		// Chat Completions has no image slot on a role:"tool" message, so an
		// image-bearing result (view_image) degrades to text with a placeholder
		// rather than dropping the blocks silently.
		text := agentcore.ContentToText(msg.Content)
		if contentHasImages(msg.Content) {
			if text != "" {
				text += "\n"
			}
			text += "[image content omitted: this provider protocol cannot carry images in tool results]"
		}
		return []map[string]any{{
			"role":         "tool",
			"tool_call_id": msg.ToolCallID,
			"content":      text,
		}}
	default:
		return nil
	}
}

// openAIUserContent shapes a user content list for the OpenAI wire. When there
// are no images it collapses to a plain string (the common case, and what most
// OpenAI-compatible gateways expect). When images are present it emits the
// multimodal array form: text parts plus image_url parts carrying a base64 data
// URI (data:<mime>;base64,<data>).
func openAIUserContent(content agentcore.ContentList) any {
	hasImage := false
	for _, c := range content {
		if _, ok := c.(agentcore.ImageContent); ok {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return agentcore.ContentToText(content)
	}
	parts := make([]map[string]any, 0, len(content))
	for _, c := range content {
		switch b := c.(type) {
		case agentcore.TextContent:
			if b.Text == "" {
				continue
			}
			parts = append(parts, map[string]any{"type": "text", "text": b.Text})
		case agentcore.ImageContent:
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": fmt.Sprintf("data:%s;base64,%s", b.MimeType, b.Data),
				},
			})
		}
	}
	return parts
}

// encodeOpenAITools maps AgentTools onto the OpenAI function-tool schema.
func encodeOpenAITools(tools []agentcore.AgentTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := json.RawMessage(t.Schema())
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  params,
			},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Anthropic-compatible driver (Bedrock).
// ---------------------------------------------------------------------------

// anthropicCompatDriver backs Anthropic-wire providers that are not the direct
// Anthropic API — Bedrock being the case here. It POSTs the Messages wire
// format and decodes with AnthropicDecoder.
type anthropicCompatDriver struct {
	name    string
	baseURL string
	models  []Model
	// path is the endpoint path appended to baseURL (Bedrock's invoke path
	// embeds the model id, so it is derived per request).
	pathFor func(model string) string
	// authHeader sets provider auth on the request (never logs the value).
	authHeader func(req *http.Request, apiKey string)
}

func (d *anthropicCompatDriver) Name() string    { return d.name }
func (d *anthropicCompatDriver) Models() []Model { return d.models }

// StreamCompletion builds the Anthropic Messages request and streams it,
// decoding with AnthropicDecoder.
func (d *anthropicCompatDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if strings.TrimSpace(req.Config.APIKey) == "" {
		return nil, fmt.Errorf("%s: missing API key (set %s)", d.name, APIKeyEnvHint(d.name))
	}
	if err := checkImageSupport(d.name, req.Model, d.models, req.Context.Messages); err != nil {
		return nil, err
	}
	plan := anthropicThinkingPlanFor(d.name, req.Model, req.Config.ThinkingLevel)
	body, err := encodeAnthropicRequestWithPlan(req, d.models, plan)
	if err != nil {
		return nil, fmt.Errorf("%s: build request body: %w", d.name, err)
	}
	path := "/messages"
	if d.pathFor != nil {
		path = d.pathFor(req.Model)
	}
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
		// opencode's gateways route by client session id and refuse a Messages
		// request without it (400 MissingSessionID: "cannot be routed
		// efficiently"), the same header the chat and Responses drivers attach
		// (see SessionHeaderValue). Gated on the provider name there, so only
		// opencode-* providers are decorated.
		if sid := SessionHeaderValue(d.name, req.Config.Extra); sid != "" {
			httpReq.Header.Set(OpencodeSessionHeader, sid)
		}
		return httpReq, nil
	}
	return StreamRequest(ctx, TransportConfig{
		Client:     clientForURL(d.name, d.baseURL),
		NewRequest: newReq,
		Decoder:    NewAnthropicDecoder(),
		// A model newer than the catalog still gets the thinking body it accepts:
		// the gateway names the adaptive form in a 400 and the request is
		// re-issued once with it (see thinkingShapeCorrection).
		RetryBody: d.thinkingShapeCorrection(req, plan, &body),
	})
}

// encodeAnthropicRequest serializes a CompletionRequest into an Anthropic
// Messages JSON body with streaming enabled. The system prompt is a top-level
// field; tool results and tool calls follow the Messages content-block shape.
//
// providerName keys the reasoning catalog, which decides which thinking shape
// the model accepts (see anthropicThinkingPlanFor); the same answer decides
// whether history's thinking blocks may be echoed.
func encodeAnthropicRequest(providerName string, req CompletionRequest, models []Model) ([]byte, error) {
	return encodeAnthropicRequestWithPlan(req, models,
		anthropicThinkingPlanFor(providerName, req.Model, req.Config.ThinkingLevel))
}

// encodeAnthropicRequestWithPlan builds the Messages body for an already-decided
// thinking plan. The recovery path re-encodes the same request after a gateway
// named the shape it accepts; going through the plan keeps one body builder, so
// the corrected request cannot drift from the original.
func encodeAnthropicRequestWithPlan(req CompletionRequest, models []Model, plan anthropicThinkingPlan) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Context.Messages))
	for _, m := range req.Context.Messages {
		if enc := encodeAnthropicMessage(m, plan.thinking != nil); enc != nil {
			msgs = append(msgs, enc)
		}
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	if sp := req.Context.SystemPrompt; sp != "" {
		body["system"] = sp
	}
	maxTok := maxOutputTokensFor(req)
	if maxTok <= 0 {
		// Anthropic requires max_tokens. Prefer the model's declared cap; fall
		// back to a coding-friendly default (4096 was too low and caused
		// truncation/retry loops on longer edits).
		maxTok = anthropicDefaultMaxTokens(req.Model, models)
	}
	if plan.budget > 0 {
		// Anthropic counts thinking tokens toward max_tokens and requires
		// budget_tokens < max_tokens (else a 400). Guarantee headroom for the
		// visible reply by lifting max_tokens above the budget when the caller's
		// cap is too low to fit both the reasoning and a real answer.
		if minTok := plan.budget + anthropicResponseHeadroom; maxTok < minTok {
			maxTok = minTok
		}
	}
	body["max_tokens"] = maxTok
	if plan.thinking != nil {
		body["thinking"] = plan.thinking
	}
	if plan.effort != "" {
		body["output_config"] = map[string]any{"effort": plan.effort}
	}
	if tools := encodeAnthropicTools(req.Context.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	return json.Marshal(body)
}

// anthropicResponseHeadroom is the token margin reserved for the visible reply
// on top of the thinking budget, so max_tokens always exceeds budget_tokens (an
// Anthropic hard requirement) with room left for a real answer.
const anthropicResponseHeadroom = 4096

// anthropicThinkingPlan is the thinking control one Messages request carries.
type anthropicThinkingPlan struct {
	// thinking is the body's "thinking" value, or nil when the request sends no
	// thinking control at all (level off or unset).
	thinking map[string]any
	// effort is the output_config.effort value for the adaptive shape.
	effort string
	// budget is the legacy shape's budget_tokens; only that shape must fit under
	// max_tokens.
	budget int
}

// anthropicThinkingPlanFor selects the thinking body shape for a (provider,
// model) pair at the given level. Which shape a model accepts is its own
// property, not golder's preference:
//
//   - effort-shaped (models.dev lists an effort ladder and no legacy budget):
//     {"type":"adaptive"} plus output_config.effort, the level clamped into
//     that ladder. claude-haiku-5-5 rejects the legacy token-budget body
//     outright ("thinking.type.enabled is not supported for this model", probed
//     live), so this is the only shape that turns its thinking on at all.
//   - toggle-shaped (on/off only): the adaptive switch, with no level to name.
//   - budget-shaped, or a model the catalog does not cover: the legacy
//     {"type":"enabled","budget_tokens":N} body golder has always sent. An
//     unknown model keeps the historical shape rather than being guessed at.
func anthropicThinkingPlanFor(providerName, model string, level agentcore.ThinkingLevel) anthropicThinkingPlan {
	if anthropicThinkingBudget(level) <= 0 { // off or unset
		return anthropicThinkingPlan{}
	}
	// A gateway that already rejected the legacy body wins over the catalog: the
	// model is newer than golder's copy of it (see thinkingShapeCorrection).
	if effortThinkingForced(providerName, model) {
		return anthropicThinkingPlan{
			thinking: map[string]any{"type": "adaptive"},
			effort:   adaptiveEffort(providerName, model, level),
		}
	}
	switch shape, ladder := ReasoningControl(providerName, model); shape {
	case ReasoningShapeEffort:
		return anthropicThinkingPlan{
			thinking: map[string]any{"type": "adaptive"},
			effort:   clampEffort(level, ladder),
		}
	case ReasoningShapeToggle:
		return anthropicThinkingPlan{thinking: map[string]any{"type": "adaptive"}}
	default:
		budget := anthropicThinkingBudget(level)
		return anthropicThinkingPlan{
			thinking: map[string]any{"type": "enabled", "budget_tokens": budget},
			budget:   budget,
		}
	}
}

// thinkingShapeCorrection returns the transport's RetryBody hook for a request
// whose thinking body was the legacy token-budget one: a model newer than the
// catalog rejects it with a message naming the adaptive form, and the same
// request is re-issued once with that body — and the verdict remembered for the
// rest of the process. It returns nil when there is nothing to correct
// (thinking off, or the adaptive shape already sent).
func (d *anthropicCompatDriver) thinkingShapeCorrection(req CompletionRequest, plan anthropicThinkingPlan, body *[]byte) func(int, []byte) bool {
	if plan.thinking == nil || plan.effort != "" {
		return nil
	}
	return func(status int, respBody []byte) bool {
		if status != http.StatusBadRequest || !bytes.Contains(respBody, []byte(effortShapeHint)) {
			return false
		}
		corrected, err := encodeAnthropicRequestWithPlan(req, d.models, anthropicThinkingPlan{
			thinking: map[string]any{"type": "adaptive"},
			effort:   adaptiveEffort(d.name, req.Model, req.Config.ThinkingLevel),
		})
		if err != nil {
			return false
		}
		*body = corrected
		rememberEffortThinking(d.name, req.Model)
		return true
	}
}

// effortShapeHint is the phrase the Messages API uses when it rejects the legacy
// token-budget thinking body and names the adaptive one instead (probed live:
// `"thinking.type.enabled" is not supported for this model. Use
// "thinking.type.adaptive" and "output_config.effort"`). Anchored on the type
// name so an unrelated 400 — a bad tool schema, an oversized prompt — never
// triggers the correction.
const effortShapeHint = "thinking.type.adaptive"

// effortThinkingVerdicts remembers (provider, model) pairs whose gateway has
// said it wants the adaptive thinking body. It is process state: the catalog
// cache is the durable answer, and this covers the window before it is fetched —
// a one-shot run may never fetch models.dev at all.
var effortThinkingVerdicts sync.Map

// rememberEffortThinking records the gateway's verdict for (provider, model).
func rememberEffortThinking(providerName, model string) {
	if providerName == "" || model == "" {
		return
	}
	effortThinkingVerdicts.Store(providerName+"\x00"+model, struct{}{})
}

// effortThinkingForced reports whether this process has already been told that
// (provider, model) takes the adaptive thinking body.
func effortThinkingForced(providerName, model string) bool {
	_, ok := effortThinkingVerdicts.Load(providerName + "\x00" + model)
	return ok
}

// adaptiveEffortLadder is the effort set the adaptive-thinking API accepts, used
// when the catalog has no ladder for the model — the recovery path, where the
// gateway named the shape but golder has never seen the model's own values
// (probed: haiku-5-5 answers "Input should be 'low', 'medium', 'high', 'xhigh'
// or 'max'" for anything outside it).
var adaptiveEffortLadder = []agentcore.ThinkingLevel{
	agentcore.ThinkingLow, agentcore.ThinkingMedium, agentcore.ThinkingHigh,
	agentcore.ThinkingXHigh, agentcore.ThinkingMax,
}

// adaptiveEffort maps a level onto the effort value to send: the model's own
// catalog ladder when it has one, else the adaptive API's own set.
func adaptiveEffort(providerName, model string, level agentcore.ThinkingLevel) string {
	ladder := KnownReasoningLevels(providerName, model)
	if len(ladder) == 0 {
		ladder = adaptiveEffortLadder
	}
	return clampEffort(level, ladder)
}

// clampEffort picks the effort value to send for level from a model's advertised
// ladder (ascending): the level itself when it is allowed, else the closest rung
// below it, else the lowest rung. An empty ladder returns the level verbatim —
// the catalog said the model takes effort control without naming its values, and
// the gateway is the authority on what it accepts.
func clampEffort(level agentcore.ThinkingLevel, ladder []agentcore.ThinkingLevel) string {
	if len(ladder) == 0 {
		return string(level)
	}
	best := ladder[0]
	for _, l := range ladder {
		if reasoningRank[l] <= reasoningRank[level] {
			best = l
		}
	}
	return string(best)
}

// anthropicDefaultMaxTokens picks the max_tokens fallback when no explicit hint
// is given: the model's declared MaxOutputTokens if present in the driver's
// model catalog, otherwise 8192 (a coding-friendly default that avoids
// premature truncation while staying within common model caps).
func anthropicDefaultMaxTokens(model string, models []Model) int {
	for _, m := range models {
		if m.ID == model && m.MaxOutputTokens > 0 {
			return m.MaxOutputTokens
		}
	}
	return 8192
}

// anthropicThinkingBudget maps a unified ThinkingLevel onto an Anthropic
// thinking budget_tokens value. off/"" yields 0 (thinking block omitted).
func anthropicThinkingBudget(level agentcore.ThinkingLevel) int {
	switch level {
	case agentcore.ThinkingMinimal:
		return 1024
	case agentcore.ThinkingLow:
		return 2048
	case agentcore.ThinkingMedium:
		return 8192
	case agentcore.ThinkingHigh:
		return 16384
	case agentcore.ThinkingXHigh:
		return 32768
	case agentcore.ThinkingMax:
		return 65536
	default: // off or unset
		return 0
	}
}

// encodeAnthropicMessage maps one golder message onto the Anthropic Messages
// wire shape. Assistant tool calls become tool_use blocks; tool results become
// a user message carrying a tool_result block (Anthropic's convention).
//
// thinkingOn is the request's extended-thinking setting: thinking blocks ride
// the wire only when the request turns thinking on (the Messages API validates
// them against that setting), and even then only when they are ours to echo —
// see anthropicThinkingReplayable.
func encodeAnthropicMessage(m agentcore.Message, thinkingOn bool) map[string]any {
	switch msg := m.(type) {
	case agentcore.UserMessage:
		return map[string]any{"role": "user", "content": anthropicUserContent(msg.Content)}
	case agentcore.CompactionMessage:
		u := msg.AsUserMessage()
		return map[string]any{"role": "user", "content": anthropicUserContent(u.Content)}
	case agentcore.AssistantMessage:
		var blocks []map[string]any
		// Thinking blocks must precede tool_use in the same assistant turn:
		// Anthropic extended-thinking requires the prior thinking block (and its
		// signature) to be echoed back verbatim on tool-use turns, or the API
		// rejects/degrades the request. Emit them first.
		if thinkingOn {
			for _, c := range msg.Content {
				if t, ok := c.(agentcore.ThinkingContent); ok {
					if !anthropicThinkingReplayable(t) {
						continue
					}
					if t.Redacted {
						blocks = append(blocks, map[string]any{
							"type": "redacted_thinking", "data": t.ThinkingSignature,
						})
						continue
					}
					blocks = append(blocks, map[string]any{
						"type": "thinking", "thinking": t.Thinking, "signature": t.ThinkingSignature,
					})
				}
			}
		}
		for _, c := range msg.Content {
			switch b := c.(type) {
			case agentcore.TextContent:
				if b.Text == "" {
					continue
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case agentcore.ToolCallContent:
				// Provider-executed calls have no local output to pair; the
				// Messages API would reject an unpaired tool_use.
				if b.IsServer() {
					continue
				}
				var input any
				_ = json.Unmarshal(b.Arguments, &input)
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": b.ID, "name": b.Name, "input": input,
				})
			}
		}
		if len(blocks) == 0 {
			// No usable content: emit a single space rather than an empty text
			// block ("text must be non-empty" is rejected by strict endpoints).
			blocks = []map[string]any{{"type": "text", "text": " "}}
		}
		return map[string]any{"role": "assistant", "content": blocks}
	case agentcore.ToolResultMessage:
		// The content may carry images (view_image): the Messages API accepts
		// a content-block array on tool_result, so text and images both ride
		// it; a text-only result keeps the plain string form.
		return map[string]any{"role": "user", "content": []map[string]any{{
			"type":        "tool_result",
			"tool_use_id": msg.ToolCallID,
			"content":     anthropicToolResultContent(msg.Content),
		}}}
	default:
		return nil
	}
}

// anthropicThinkingReplayable reports whether a stored thinking block may be
// echoed back on the Messages wire. The API validates an echoed thinking block
// against its own signature, and a signature is exactly the provenance evidence
// golder holds: only the Anthropic-wired decoder fills ThinkingSignature (the
// OpenAI chat driver's reasoning_content and the Responses reasoning summary
// carry none). So a block with a signature (or, for a redacted block, the
// encrypted data that lives in the same field) is ours to replay, and anything
// else — another protocol's reasoning that happens to be in the history after a
// /model or /provider switch, or a truncation that emptied it — is dropped
// rather than sent as a signature-less thinking block the API would reject, and
// rather than presenting another model's reasoning as this conversation's.
func anthropicThinkingReplayable(t agentcore.ThinkingContent) bool {
	if t.ThinkingSignature == "" {
		return false
	}
	if t.Redacted {
		return true // the payload is the data field; there is no display text
	}
	return t.Thinking != ""
}

// anthropicToolResultContent renders a tool result's content for a tool_result
// block: plain text when it carries no images, otherwise a block array with
// text plus base64 image sources (the shape the Messages API expects).
func anthropicToolResultContent(content agentcore.ContentList) any {
	var images []agentcore.ImageContent
	for _, c := range content {
		if img, ok := c.(agentcore.ImageContent); ok {
			images = append(images, img)
		}
	}
	if len(images) == 0 {
		return agentcore.ContentToText(content)
	}
	blocks := make([]map[string]any, 0, len(content))
	if text := agentcore.ContentToText(content); strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MimeType,
				"data":       img.Data,
			},
		})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": " "})
	}
	return blocks
}

// contentHasImages reports whether a content list carries an image block.
func contentHasImages(content agentcore.ContentList) bool {
	for _, c := range content {
		if _, ok := c.(agentcore.ImageContent); ok {
			return true
		}
	}
	return false
}

// anthropicUserContent shapes a user content list for the Anthropic Messages
// wire. When there are no images it collapses to a plain string. When images
// are present it emits the content-block array form: text blocks plus image
// blocks with a base64 source ({"type":"image","source":{"type":"base64",
// "media_type":<mime>,"data":<b64>}}).
func anthropicUserContent(content agentcore.ContentList) any {
	hasImage := false
	for _, c := range content {
		if _, ok := c.(agentcore.ImageContent); ok {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return agentcore.ContentToText(content)
	}
	blocks := make([]map[string]any, 0, len(content))
	for _, c := range content {
		switch b := c.(type) {
		case agentcore.TextContent:
			if b.Text == "" {
				continue
			}
			blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
		case agentcore.ImageContent:
			blocks = append(blocks, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": b.MimeType,
					"data":       b.Data,
				},
			})
		}
	}
	return blocks
}

// contextHasImage reports whether any message in the context carries an image
// content block, so a driver can reject image input on a non-multimodal model.
func contextHasImage(msgs []agentcore.Message) bool {
	for _, m := range msgs {
		var content agentcore.ContentList
		switch msg := m.(type) {
		case agentcore.UserMessage:
			content = msg.Content
		case agentcore.CompactionMessage:
			content = msg.AsUserMessage().Content
		default:
			continue
		}
		for _, c := range content {
			if _, ok := c.(agentcore.ImageContent); ok {
				return true
			}
		}
	}
	return false
}

// checkImageSupport returns a clear error when the request carries image input
// but the named model (looked up in models) does not declare SupportsImages.
// A model absent from the catalog is treated permissively (unknown capability),
// deferring to the provider's own validation. This turns the silent drop of
// image blocks on a text-only model into an actionable message.
func checkImageSupport(providerName, model string, models []Model, msgs []agentcore.Message) error {
	if !contextHasImage(msgs) {
		return nil
	}
	for _, m := range models {
		if m.ID == model {
			if !m.SupportsImages {
				return fmt.Errorf("%s: model %q does not support image input", providerName, model)
			}
			return nil
		}
	}
	return nil
}

// encodeAnthropicTools maps AgentTools onto the Anthropic tool schema.
func encodeAnthropicTools(tools []agentcore.AgentTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := json.RawMessage(t.Schema())
		if len(schema) == 0 {
			schema = json.RawMessage("{}")
		}
		out = append(out, map[string]any{
			"name":         t.Name(),
			"description":  t.Description(),
			"input_schema": schema,
		})
	}
	return out
}

// maxOutputTokensFor pulls a max-output hint from the request Extra map, if any,
// so callers can bound Anthropic responses without a registry lookup here.
func maxOutputTokensFor(req CompletionRequest) int {
	if req.Config.Extra == nil {
		return 0
	}
	switch v := req.Config.Extra["max_tokens"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Constructors.
// ---------------------------------------------------------------------------

// openAICompatPreset captures the per-gateway differences among the
// OpenAI-compatible providers: everything the constructors used to repeat
// (provider name, default endpoint, whether auth is required, and any extra
// headers). Collapsing the near-identical constructors onto this table means
// "add a gateway" is a one-line entry plus a thin exported wrapper.
type openAICompatPreset struct {
	name         string
	defaultURL   string // "" ⇒ no default; baseURL must be supplied by the caller
	requiresAuth bool
	extraHeaders map[string]string
}

// newOpenAICompat builds an OpenAI-compatible driver from a preset, falling back
// to the preset's default endpoint when baseURL is empty.
func newOpenAICompat(p openAICompatPreset, baseURL string, models []Model) Provider {
	if baseURL == "" {
		baseURL = p.defaultURL
	}
	return &openAICompatDriver{
		name:         p.name,
		baseURL:      baseURL,
		models:       models,
		requiresAuth: p.requiresAuth,
		extraHeaders: p.extraHeaders,
	}
}

// NewOpenRouterProvider builds the OpenRouter provider — the reference
// OpenAI-compatible gateway. baseURL defaults to the public endpoint when empty.
func NewOpenRouterProvider(baseURL string, models []Model) Provider {
	return newOpenAICompat(openAICompatPreset{
		name:         "openrouter",
		defaultURL:   openRouterBaseURL,
		requiresAuth: true,
		extraHeaders: map[string]string{
			// OpenRouter attribution headers (optional but recommended).
			"HTTP-Referer": "https://github.com/getan/golder",
			"X-Title":      "golder",
		},
	}, baseURL, models)
}

// NewOllamaProvider builds the Ollama provider (local, OpenAI-compatible, no
// auth). baseURL defaults to the local daemon when empty.
func NewOllamaProvider(baseURL string, models []Model) Provider {
	return newOpenAICompat(openAICompatPreset{
		name:         "ollama",
		defaultURL:   ollamaBaseURL,
		requiresAuth: false,
	}, baseURL, models)
}

// NewNvidiaProvider builds the NVIDIA provider (hosted NIM, OpenAI-compatible,
// Bearer auth). baseURL defaults to the public integrate endpoint when empty.
// The API key is resolved by the "nvidia" provider name (NVIDIA_API_KEY);
// secret values are never logged.
func NewNvidiaProvider(baseURL string, models []Model) Provider {
	return newOpenAICompat(openAICompatPreset{
		name:         "nvidia",
		defaultURL:   nvidiaBaseURL,
		requiresAuth: true,
	}, baseURL, models)
}

// NewOpenAICompatibleProvider builds a generic OpenAI-compatible provider for an
// arbitrary gateway reached by baseURL (Bearer auth). It is the target of an
// explicit --protocol=openai selection: unlike the preset constructors it has no
// default endpoint (baseURL must be supplied) and carries the neutral provider
// name "openai", so an API key resolves from OPENAI_API_KEY (or the --api-key
// override bound to that name). Secret values are never logged.
func NewOpenAICompatibleProvider(baseURL string, models []Model) Provider {
	return newOpenAICompat(openAICompatPreset{
		name:         "openai",
		defaultURL:   "", // no default: caller must supply the endpoint
		requiresAuth: true,
	}, baseURL, models)
}

// NewOpenAICompatibleProviderAs builds the same generic driver carrying the
// given provider name, so errors and per-provider request decoration (e.g. the
// x-opencode-session header) reference the selected provider instead of the
// neutral "openai". ResolveNamedProvider uses it for every OpenAI-wired
// registry entry; API-key resolution still keys on the returned provider name
// upstream and is unaffected.
func NewOpenAICompatibleProviderAs(name, baseURL string, models []Model) Provider {
	return newOpenAICompat(openAICompatPreset{
		name:         name,
		defaultURL:   "", // no default: caller must supply the endpoint
		requiresAuth: true,
	}, baseURL, models)
}

// newAnthropicCompat builds an Anthropic-Messages driver, falling back to
// defaultURL when baseURL is empty. It is the shared body of the two
// Anthropic-wire constructors, which differ only in name, default endpoint, and
// auth header.
func newAnthropicCompat(name, defaultURL, baseURL string, models []Model, authHeader func(*http.Request, string)) Provider {
	if baseURL == "" {
		baseURL = defaultURL
	}
	return &anthropicCompatDriver{
		name:       name,
		baseURL:    baseURL,
		models:     models,
		authHeader: authHeader,
	}
}

// anthropicAuthHeaderFor returns the auth-header setter for an Anthropic-Messages
// provider given its registry AuthScheme (spec.AuthScheme). The two shapes seen
// among anthropic-protocol providers are:
//
//   - AuthBearer      → Authorization: Bearer <key>. Used by anthropic-protocol
//     gateways that authenticate with a plain bearer token on their /anthropic
//     endpoint.
//   - AuthXAPIKey     → x-api-key: <key> plus the required anthropic-version
//     header. This is the direct-Anthropic convention and, per pi's behavior,
//     also what MiniMax (minimax / minimax-cn) uses on its /anthropic endpoint.
//
// Any other scheme (e.g. AuthAWS for Bedrock, AuthSpecial) falls back to the
// x-api-key convention so a generic anthropic-protocol provider does not crash;
// bespoke auth for those is layered by a later node. The returned func never
// logs the secret value.
func anthropicAuthHeaderFor(authScheme string) func(*http.Request, string) {
	if authScheme == AuthBearer {
		return func(req *http.Request, apiKey string) {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	return func(req *http.Request, apiKey string) {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
	}
}

// NewAnthropicProvider builds a provider that speaks the Anthropic Messages wire
// format directly (POST {baseURL}/messages), the target of an explicit
// --protocol=anthropic selection. baseURL defaults to the public Anthropic API
// when empty. Auth uses the Anthropic conventions: an x-api-key header plus the
// required anthropic-version header. The API key resolves by the "anthropic"
// provider name (ANTHROPIC_API_KEY / CLAUDE_API_KEY, or the --api-key override);
// secret values are never logged.
func NewAnthropicProvider(baseURL string, models []Model) Provider {
	return newAnthropicCompat("anthropic", anthropicBaseURL, baseURL, models,
		anthropicAuthHeaderFor(AuthXAPIKey))
}

// NewAnthropicProtocolProvider builds a named Anthropic-Messages provider whose
// auth header follows the given registry AuthScheme. It is the target of an
// explicit --provider selection for any anthropic-protocol built-in (anthropic,
// minimax, minimax-cn, and — routed generically for now — bedrock/
// cloudflare-ai-gateway): the driver identity is the provider's own name (so
// errors reference it), baseURL is the already-resolved endpoint (spec default
// or override), and authScheme selects the header shape (see
// anthropicAuthHeaderFor). Secret values are never logged.
func NewAnthropicProtocolProvider(name, baseURL, authScheme string, models []Model) Provider {
	return newAnthropicCompat(name, anthropicBaseURL, baseURL, models,
		anthropicAuthHeaderFor(authScheme))
}

// NewBedrockProvider builds the Bedrock provider, reusing the Anthropic Messages
// decoder (Anthropic-on-Bedrock speaks the Messages wire format). baseURL
// defaults to a us-east-1 runtime endpoint when empty; real deployments pass
// the region-specific URL. Auth is a Bearer token (Bedrock API keys); SigV4
// signing, when required, is layered by the caller's HTTP client.
func NewBedrockProvider(baseURL string, models []Model) Provider {
	return newAnthropicCompat("bedrock", bedrockBaseURL, baseURL, models,
		func(req *http.Request, apiKey string) {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		})
}
