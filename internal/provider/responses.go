// This file implements the OpenAI Responses API driver (US-003, #539), the
// backing for --protocol openai/resp_api. Unlike openAICompatDriver (which
// hand-rolls the Chat Completions wire format), this driver speaks the
// Responses API (POST {base_url}/responses) via the official
// github.com/openai/openai-go SDK.
//
// This milestone covers streaming text: a plain prompt in, assistant text out,
// consumed from the Responses SSE stream and mapped into golder's AssistantMessage
// the same way OpenAIDecoder does (API/Provider tags, Usage,
// ResponseID/ResponseModel, StopReason=end_turn). Tools (#541) and
// images/reasoning (#542) layer on later.
//
// Failure model (FR-13): only the earliest "cannot build the stream" case
// (missing API key) is a returned error. Every runtime failure — including a
// non-2xx from the endpoint — rides the returned stream as a terminal
// StreamErrorEvent, matching the chat driver's observable behavior.
//
// Base URL + auth: the SDK client is pointed at the resolved base_url and given
// the resolved key via option.WithBaseURL / option.WithAPIKey rather than
// reading the environment, so ResolveBaseURL precedence is preserved. A custom
// *http.Client may be injected (option.WithHTTPClient) for tests.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"github.com/getan/golder/internal/agentcore"
)

// responsesDriver is the Provider backing --protocol openai/resp_api. It holds
// the provider identity, the resolved endpoint, the model catalog, and whether
// an API key is required, and builds an openai-go client per request.
type responsesDriver struct {
	name    string
	baseURL string
	models  []Model
	// requiresAuth reports whether an API key must be present. Public OpenAI /
	// Azure require it; a local gateway may not.
	requiresAuth bool
	// clientOpts are extra SDK options; tests inject option.WithHTTPClient here
	// to stub the transport.
	clientOpts []option.RequestOption
}

// NewOpenAIResponsesProvider builds a Responses API provider targeting baseURL.
// baseURL must be the fully resolved endpoint (e.g. https://api.openai.com/v1);
// the SDK appends the /responses path.
func NewOpenAIResponsesProvider(name, baseURL string, models []Model) *responsesDriver {
	return &responsesDriver{
		name:         name,
		baseURL:      baseURL,
		models:       models,
		requiresAuth: true,
	}
}

func (d *responsesDriver) Name() string    { return d.name }
func (d *responsesDriver) Models() []Model { return d.models }

// StreamCompletion issues a streaming Responses API call and surfaces the result
// on an AssistantMessageEventStream: a start event, incremental text events as
// deltas arrive, and a terminal done event carrying the aggregated message.
func (d *responsesDriver) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	if d.requiresAuth && strings.TrimSpace(req.Config.APIKey) == "" {
		// Early "cannot build the stream": reference the provider, never a value.
		return nil, fmt.Errorf("%s: missing API key (set %s)", d.name, APIKeyEnvHint(d.name))
	}

	opts := make([]option.RequestOption, 0, len(d.clientOpts)+2)
	if d.baseURL != "" {
		opts = append(opts, option.WithBaseURL(d.baseURL))
	}
	if key := strings.TrimSpace(req.Config.APIKey); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if sid := SessionHeaderValue(d.name, req.Config.Extra); sid != "" {
		opts = append(opts, option.WithHeader(OpencodeSessionHeader, sid))
	}
	if pc := clientForURL(d.name, d.baseURL); pc != nil {
		opts = append(opts, option.WithHTTPClient(pc))
	}
	opts = append(opts, d.clientOpts...)
	client := openai.NewClient(opts...)

	stream := NewAssistantMessageEventStream(0)
	go d.pump(ctx, stream, &client, req)
	return stream, nil
}

// pump consumes the Responses SSE stream and translates events into golder stream
// events. It always closes the stream. Every runtime failure (transport error,
// context cancellation, or an upstream error/failed event) becomes a terminal
// StreamErrorEvent (dual failure model), not a returned error.
//
// Incremental text.delta events emit a StreamTextEvent carrying the accumulated
// text so far, so the TUI renders tokens as they arrive. The terminal message is
// built from the authoritative response.completed payload via mapResponse, so
// the final aggregation matches the non-streamed result exactly. If no completed
// event arrives (a truncated stream that still ended cleanly), the accumulated
// delta text is used as a fallback.
func (d *responsesDriver) pump(ctx context.Context, stream *AssistantMessageEventStream, client *openai.Client, req CompletionRequest) {
	defer stream.Close()

	if err := stream.Emit(ctx, StreamStartEvent{Partial: d.newPartial()}); err != nil {
		return
	}

	// Probe-and-fallback: declare the hosted web_search tool unless a previous
	// turn cached a negative for this (provider, model), and only for model
	// families that can actually call it (see hostedSearchCapable — a gateway
	// that accepts the declaration without delivering the tool leaves the model
	// with no search at all, which no 400 ever reveals). A capability-shaped
	// 400 retries once with the local websearch function tool instead.
	useHosted := len(req.Context.Tools) > 0 &&
		hostedSearchCapable(req.Model) &&
		!hostedSearchKnownUnsupported(d.name, req.Model)
	seen := citedURLs(req.Context.Messages)
	for attempt := 0; ; attempt++ {
		params := buildResponsesParams(d.name, req, useHosted)
		msg, empty, err := d.runOnce(ctx, stream, client, params, seen)
		if err == nil {
			stream.Emit(ctx, StreamDoneEvent{Message: msg})
			return
		}
		// Retry only a pristine first attempt: any accumulated content means
		// the failure came mid-stream, where resending would duplicate output.
		if attempt == 0 && useHosted && empty && isHostedSearchUnsupportedErr(err) {
			markHostedSearchUnsupported(d.name, req.Model)
			useHosted = false
			continue
		}
		d.emitError(stream, err)
		return
	}
}

// runOnce consumes one Responses SSE stream, emitting text/thinking/tool-call
// partials as they arrive, and returns the terminal message (from the
// authoritative response.completed payload) or the failure.
func (d *responsesDriver) runOnce(ctx context.Context, stream *AssistantMessageEventStream, client *openai.Client, params responses.ResponseNewParams, seen map[string]bool) (agentcore.AssistantMessage, bool, error) {
	sse := client.Responses.NewStreaming(ctx, params)
	defer sse.Close()

	var text strings.Builder
	var thinking strings.Builder
	var toolCalls []agentcore.ToolCallContent
	var completed *responses.Response
	empty := func() bool {
		return text.Len() == 0 && thinking.Len() == 0 && len(toolCalls) == 0 && completed == nil
	}
	emitPartial := func() error {
		partial := d.buildPartial(thinking.String(), text.String(), toolCalls)
		if err := stream.Emit(ctx, StreamToolCallEvent{Partial: partial}); err != nil {
			return err
		}
		return nil
	}
	for sse.Next() {
		if ctx.Err() != nil {
			return agentcore.AssistantMessage{}, empty(), ctx.Err()
		}
		switch variant := sse.Current().AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			text.WriteString(variant.Delta)
			partial := d.buildPartial(thinking.String(), text.String(), toolCalls)
			if err := stream.Emit(ctx, StreamTextEvent{Partial: partial}); err != nil {
				return agentcore.AssistantMessage{}, empty(), err
			}
		case responses.ResponseReasoningSummaryTextDeltaEvent:
			// The model's reasoning summary streams as its own text deltas, distinct
			// from the answer text; accumulate it into a thinking block so the TUI
			// renders reasoning the same way the chat driver does.
			thinking.WriteString(variant.Delta)
			partial := d.buildPartial(thinking.String(), text.String(), toolCalls)
			if err := stream.Emit(ctx, StreamThinkingEvent{Partial: partial}); err != nil {
				return agentcore.AssistantMessage{}, empty(), err
			}
		case responses.ResponseOutputItemDoneEvent:
			// A finalized function_call item carries the model's tool request
			// (name + arguments + call_id); a web_search_call item records a
			// hosted search as a server-executed call (display + native replay,
			// never local execution). Either way surface a tool-call partial so
			// the TUI can show the pending call before the run ends.
			appended := false
			if fc := variant.Item.AsFunctionCall(); fc.Type == "function_call" {
				toolCalls = append(toolCalls, toolCallContent(fc))
				appended = true
			} else if ws := variant.Item.AsWebSearchCall(); ws.Type == "web_search_call" {
				if sc, ok := webSearchServerCall(ws); ok {
					toolCalls = append(toolCalls, sc)
					appended = true
				}
			}
			if appended {
				if err := emitPartial(); err != nil {
					return agentcore.AssistantMessage{}, empty(), err
				}
			}
		case responses.ResponseCompletedEvent:
			r := variant.Response
			completed = &r
		case responses.ResponseFailedEvent:
			return agentcore.AssistantMessage{}, empty(), failedResponseError(variant.Response)
		case responses.ResponseErrorEvent:
			return agentcore.AssistantMessage{}, empty(), fmt.Errorf("%s", variant.Message)
		}
	}
	if err := sse.Err(); err != nil {
		return agentcore.AssistantMessage{}, empty(), err
	}

	var msg agentcore.AssistantMessage
	if completed != nil {
		msg = d.mapResponse(completed, seen)
	} else {
		msg = d.buildPartial(thinking.String(), text.String(), toolCalls)
		msg.StopReason = agentcore.StopReasonEndTurn
		if len(toolCalls) > 0 {
			msg.StopReason = agentcore.StopReasonToolUse
		}
	}
	return msg, empty(), nil
}

// failedResponseError renders a response.failed event's embedded error: the
// server always ships a code + message (rate limits, upstream failures,
// invalid parameters), and dropping them leaves only a bare "response failed"
// that cannot be diagnosed. The response id is included for provider support.
func failedResponseError(r responses.Response) error {
	if r.Error.Message != "" {
		if r.ID != "" {
			return fmt.Errorf("response failed [%s]: %s: %s", r.ID, r.Error.Code, r.Error.Message)
		}
		return fmt.Errorf("response failed: %s: %s", r.Error.Code, r.Error.Message)
	}
	return fmt.Errorf("response failed")
}

// buildPartial assembles a cumulative snapshot message for a streaming partial:
// an optional thinking block (reasoning summary so far), the accumulated answer
// text, then any finalized tool calls — in the order the TUI should render them.
// All four emit sites in pump build partials through this one helper so they
// can't diverge.
func (d *responsesDriver) buildPartial(thinking, text string, toolCalls []agentcore.ToolCallContent) agentcore.AssistantMessage {
	msg := d.newPartial()
	if thinking != "" {
		msg.Content = append(msg.Content, agentcore.NewThinkingContent(thinking))
	}
	if text != "" {
		msg.Content = append(msg.Content, agentcore.NewTextContent(text))
	}
	msg.Content = appendToolCalls(msg.Content, toolCalls)
	return msg
}

// emitError emits a terminal StreamErrorEvent tagged for this provider. Uses a
// background context so the emit isn't dropped when ctx is already cancelled.
//
// A cancellation is filed as ABORTED rather than error: this path is reached
// when the caller's ctx is cancelled (an interrupt), and reporting the user's
// own keystroke as a provider error is what made Ctrl+C read as a malfunction.
func (d *responsesDriver) emitError(stream *AssistantMessageEventStream, err error) {
	reason := agentcore.StopReasonError
	text := err.Error()
	if errors.Is(err, context.Canceled) {
		reason = agentcore.StopReasonAborted
		// The front-end prints its own interrupt notice for this stop reason;
		// "context canceled" is the machinery talking.
		text = ""
	}
	stream.Emit(context.Background(), StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			API:          "openai",
			Provider:     d.name,
			StopReason:   reason,
			ErrorMessage: text,
		},
		Err: fmt.Errorf("%s: %w", d.name, err),
	})
}

// newPartial builds an empty assistant message tagged for this provider, the
// seed for start/text partials (mirrors OpenAIDecoder.partial()'s identity).
func (d *responsesDriver) newPartial() agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		API:       "openai",
		Provider:  d.name,
	}
}

// mapResponse materializes a completed Responses API result into golder's
// AssistantMessage: a reasoning summary (as a thinking block, when present),
// text content, tool calls, usage (when present), diagnostics, and a stop reason
// (tool_use when the model requested a tool, otherwise end_turn).
func (d *responsesDriver) mapResponse(resp *responses.Response, seen map[string]bool) agentcore.AssistantMessage {
	msg := d.newPartial()
	msg.StopReason = agentcore.StopReasonEndTurn
	msg.ResponseID = resp.ID
	msg.ResponseModel = string(resp.Model)
	if thinking := reasoningText(resp); thinking != "" {
		msg.Content = append(msg.Content, agentcore.NewThinkingContent(thinking))
	}
	if text := resp.OutputText(); text != "" {
		msg.Content = append(msg.Content, agentcore.NewTextContent(text))
	}
	var sawToolCall bool
	for _, item := range resp.Output {
		if fc := item.AsFunctionCall(); fc.Type == "function_call" {
			msg.Content = append(msg.Content, toolCallContent(fc))
			sawToolCall = true
		} else if ws := item.AsWebSearchCall(); ws.Type == "web_search_call" {
			if sc, ok := webSearchServerCall(ws); ok {
				msg.Content = append(msg.Content, sc)
			}
		}
	}
	msg.Content = appendSearchCitations(msg.Content, resp.Output, seen)
	if sawToolCall {
		msg.StopReason = agentcore.StopReasonToolUse
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		msg.Usage = &agentcore.Usage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		}
	}
	return msg
}

// toolCallContent maps a Responses function_call item into a golder
// ToolCallContent, keyed by the model's call_id so the tool result can be
// backfilled against it on the next turn. Arguments ride verbatim as raw JSON.
func toolCallContent(fc responses.ResponseFunctionToolCall) agentcore.ToolCallContent {
	return agentcore.NewToolCallContent(fc.CallID, fc.Name, json.RawMessage(fc.Arguments))
}

// reasoningText concatenates the summary text of every reasoning item in a
// completed response. The Responses API returns the model's reasoning as one or
// more reasoning items, each carrying summary parts; golder surfaces the joined
// text as a single thinking block, mirroring how the chat driver renders
// accumulated reasoning_content.
func reasoningText(resp *responses.Response) string {
	var b strings.Builder
	for _, item := range resp.Output {
		if r := item.AsReasoning(); r.Type == "reasoning" {
			for _, s := range r.Summary {
				b.WriteString(s.Text)
			}
		}
	}
	return b.String()
}

// appendToolCalls appends each accumulated tool call to a content list. Kept
// separate so the streaming partial and the terminal message build identical
// content from the same source.
func appendToolCalls(content agentcore.ContentList, calls []agentcore.ToolCallContent) agentcore.ContentList {
	for _, c := range calls {
		content = append(content, c)
	}
	return content
}

// buildResponsesParams maps a CompletionRequest onto Responses API params. The
// system prompt becomes Instructions; the thinking level becomes a reasoning
// effort (with an auto summary so reasoning is returned); golder tools become
// Responses function tools; the session id becomes the prompt_cache_key;
// and each message is replayed as the matching input item(s): assistant tool
// calls as function_call items, tool results as function_call_output items, and
// text (plus any images) as a role-tagged message.
func buildResponsesParams(providerName string, req CompletionRequest, useHostedSearch bool) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
	}
	// The model may answer with several tool calls in one turn; golder runs
	// that batch in parallel (see agenttool.ExecuteToolCalls), so the request
	// explicitly permits it. OpenAI defaults this on when tools are present,
	// but compatible Responses gateways do not always — codex sends the field
	// on every request for models that support it, and this is the same bet.
	params.ParallelToolCalls = openai.Bool(true)
	if sp := strings.TrimSpace(req.Context.SystemPrompt); sp != "" {
		params.Instructions = openai.String(sp)
	}
	if effort := WireReasoningEffort(providerName, req.Model, req.Config.ThinkingLevel); effort != "" {
		// The auto summary is what carries the model's reasoning back to golder,
		// which renders it as a thinking block, matching the chat driver. It goes
		// out unconditionally — including to OpenCode Go, whose catalog entry in
		// codex marks the parameter unsupported and where codex therefore omits it.
		// The gateway honors it (verified live: muse streams
		// reasoning_summary_text deltas only when it is requested, and without it
		// the summary comes back empty), so gating it would only drop thinking.
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(effort),
			Summary: shared.ReasoningSummaryAuto,
		}
	}
	// The per-session id doubles as prompt_cache_key: a stable key lets the
	// backend bucket one conversation's shared prefix (routing + prefix cache)
	// instead of treating every turn as unrelated. Mirrors codex, which sends
	// prompt_cache_key = session_id on every Responses request.
	if sid := requestSessionID(req.Config.Extra); sid != "" {
		params.PromptCacheKey = openai.String(sid)
	}
	if tools := buildResponsesTools(req.Context.Tools, useHostedSearch); len(tools) > 0 {
		params.Tools = tools
	}

	items := make(responses.ResponseInputParam, 0, len(req.Context.Messages))
	for _, m := range req.Context.Messages {
		items = appendInputItems(items, m, useHostedSearch)
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
	return params
}

// buildResponsesTools converts golder tools into Responses function tools. Each
// tool's JSON Schema becomes the function parameters; a schema that is empty or
// not a JSON object falls back to an empty object schema so the wire stays
// valid. Strict mode is off: golder schemas are not authored against the Responses
// strict-function contract (which requires additionalProperties:false etc.).
//
// When includeHosted is set the hosted web_search tool leads the list and the
// local websearch function tool is hidden, so the model faces exactly one
// search path; the probe-and-fallback loop swaps them back on a capability 400.
func buildResponsesTools(tools []agentcore.AgentTool, includeHosted bool) []responses.ToolUnionParam {
	if len(tools) == 0 && !includeHosted {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools)+1)
	if includeHosted {
		out = append(out, hostedSearchToolParam())
	}
	for _, t := range tools {
		if includeHosted && t.Name() == localWebSearchToolName {
			continue
		}
		params := map[string]any{}
		if raw := t.Schema(); len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				params = map[string]any{}
			}
		}
		tool := responses.ToolParamOfFunction(t.Name(), params, false)
		if desc := t.Description(); desc != "" {
			tool.OfFunction.Description = openai.String(desc)
		}
		out = append(out, tool)
	}
	return out
}

// appendInputItems replays one golder message as its Responses input item(s).
//
// hostedSearch reports whether this request declares the hosted web_search tool
// (buildResponsesTools). A server-executed call is replayed as its native
// web_search_call item only then: the item references a provider-side tool the
// current request must actually expose, and an upstream that never received
// that tool rejects the history item outright (deepseek answers 422 "Endpoint
// is unavailable"), which made every turn after a muse→deepseek model switch
// fail permanently. Without hosted search the call is dropped from the replay —
// the assistant's text (including its citations) is preserved either way.
func appendInputItems(items responses.ResponseInputParam, m agentcore.Message, hostedSearch bool) responses.ResponseInputParam {
	switch msg := m.(type) {
	case agentcore.ToolResultMessage:
		// A tool result is backfilled against the model's call_id so the model
		// can pair it with the request it issued the previous turn. A result
		// carrying images (view_image) sends them as content items
		// (input_text + input_image data URIs); the SDK's typed Output field is
		// string-only, so that one item shape is overridden with raw JSON —
		// the wire form is exactly what codex's FunctionCallOutputBody emits.
		items = append(items, toolResultFunctionCallOutput(msg))
	case agentcore.AssistantMessage:
		if text := contentText(msg.Content); text != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(
				text, responses.EasyInputMessageRoleAssistant))
		}
		for _, call := range msg.ToolCalls() {
			if call.IsServer() {
				// Hosted calls replay as their native items (no output to
				// pair) when the request declares hosted search; otherwise
				// they are dropped, as are unreplayable shapes, while the
				// answer text keeps its citations.
				if hostedSearch {
					if param, ok := webSearchCallReplayParam(call); ok {
						items = append(items, param)
					}
				}
				continue
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(
				string(call.Arguments), call.ID, call.Name))
		}
	default:
		// A user (or other non-assistant) message with images is replayed as a
		// content-part list (input_text + input_image data URIs); a text-only
		// message stays a plain string.
		if parts, ok := imageInputParts(m); ok {
			items = append(items, responses.ResponseInputItemParamOfMessage(parts, responsesRole(m.Role())))
		} else if text := messageText(m); text != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(
				text, responsesRole(m.Role())))
		}
	}
	return items
}

// toolResultFunctionCallOutput builds the function_call_output item for one
// tool result. A text-only result uses the SDK's typed helper; a result
// carrying images sends output as a content-part array instead of the string
// that helper supports, so it is overridden with raw JSON (the same wire shape
// codex's FunctionCallOutputBody uses for a view_image result).
func toolResultFunctionCallOutput(msg agentcore.ToolResultMessage) responses.ResponseInputItemUnionParam {
	var images []agentcore.ImageContent
	for _, c := range msg.Content {
		if img, ok := c.(agentcore.ImageContent); ok {
			images = append(images, img)
		}
	}
	if len(images) == 0 {
		return responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, contentText(msg.Content))
	}
	var output []map[string]any
	if text := strings.TrimSpace(contentText(msg.Content)); text != "" {
		output = append(output, map[string]any{"type": "input_text", "text": text})
	}
	for _, img := range images {
		output = append(output, map[string]any{
			"type":      "input_image",
			"image_url": fmt.Sprintf("data:%s;base64,%s", img.MimeType, img.Data),
			"detail":    "auto",
		})
	}
	raw, err := json.Marshal(map[string]any{
		"type":    "function_call_output",
		"call_id": msg.ToolCallID,
		"output":  output,
	})
	if err != nil {
		// Marshaling plain maps cannot fail; fall back to the typed text item
		// rather than dropping the result.
		return responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, contentText(msg.Content))
	}
	return param.Override[responses.ResponseInputItemUnionParam](json.RawMessage(raw))
}

// imageInputParts builds a Responses content-part list for a message that
// carries at least one image: leading input_text (the concatenated text, if
// any) followed by one input_image per image, each as a data URI. It returns
// ok=false when the message has no images, so the caller keeps the plain-text
// path.
func imageInputParts(m agentcore.Message) (responses.ResponseInputMessageContentListParam, bool) {
	content := messageContent(m)
	var hasImage bool
	for _, c := range content {
		if _, ok := c.(agentcore.ImageContent); ok {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return nil, false
	}
	parts := make(responses.ResponseInputMessageContentListParam, 0, len(content)+1)
	if text := contentText(content); text != "" {
		parts = append(parts, responses.ResponseInputContentParamOfInputText(text))
	}
	for _, c := range content {
		img, ok := c.(agentcore.ImageContent)
		if !ok {
			continue
		}
		part := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
		part.OfInputImage.ImageURL = openai.String(fmt.Sprintf("data:%s;base64,%s", img.MimeType, img.Data))
		parts = append(parts, part)
	}
	return parts, true
}

// responsesRole maps a golder message role to the Responses API input role. Tool
// results are surfaced as user turns for this text milestone.
func responsesRole(role string) responses.EasyInputMessageRole {
	switch role {
	case agentcore.RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}

// messageContent returns the content list of a message regardless of its
// concrete role type, so callers can inspect it for images.
func messageContent(m agentcore.Message) agentcore.ContentList {
	switch msg := m.(type) {
	case agentcore.UserMessage:
		return msg.Content
	case agentcore.AssistantMessage:
		return msg.Content
	case agentcore.ToolResultMessage:
		return msg.Content
	}
	return nil
}

// messageText concatenates the text blocks of a message, ignoring non-text
// content (handled in later milestones).
func messageText(m agentcore.Message) string {
	var b strings.Builder
	collectText(&b, messageContent(m))
	return b.String()
}

func collectText(b *strings.Builder, content agentcore.ContentList) {
	for _, c := range content {
		if tc, ok := c.(agentcore.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
}

// contentText concatenates the text blocks of a content list.
func contentText(content agentcore.ContentList) string {
	var b strings.Builder
	collectText(&b, content)
	return b.String()
}
