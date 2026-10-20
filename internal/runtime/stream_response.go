// This file implements streamAssistantResponse (US-003): it shapes the context
// into a provider request, resolves the API key dynamically, drives the
// provider stream, and back-fills the partial assistant message into the
// context while emitting message_start / message_update / message_end events.
package runtime

import (
	"context"
	"errors"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/redact"
)

// LoopConfig holds the pluggable behavior of the agent loop. Every hook is
// optional (nil = use the default). The pointer/func-field pattern mirrors pi's
// optional callbacks.
type LoopConfig struct {
	// Model is the model id passed to StreamFn.
	Model string
	// APIKey is the static fallback key when GetAPIKey is nil or returns "".
	APIKey string
	// ThinkingLevel is the reasoning effort for requests.
	ThinkingLevel agentcore.ThinkingLevel
	// Stream produces the provider stream. Required (defaults are wired by
	// callers/tests, e.g. a fake provider).
	Stream provider.StreamFn

	// TransformContext optionally rewrites the message list before conversion
	// (context trimming/injection). Contract: must not error; on failure return
	// a safe fallback. Runs first.
	TransformContext func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList
	// ConvertToLlm optionally filters UI-only messages. Defaults to identity.
	// Contract: must not error.
	ConvertToLlm func(msgs agentcore.MessageList) agentcore.MessageList
	// GetAPIKey optionally resolves a fresh key per request (handles short-lived
	// token expiry). Falls back to APIKey when nil or empty.
	GetAPIKey func(ctx context.Context, provider string) string
	// Provider is the provider name passed to GetAPIKey.
	Provider string

	// ContextWindow is the model's total context-token budget, used to decide
	// automatic compaction. When <= 0 the window is unknown and auto-compaction
	// is disabled (ShouldCompact returns false), so the loop behaves exactly as
	// before for callers that do not plumb it through.
	ContextWindow int
	// Compaction holds the thresholds/retention knobs for auto-compaction. Its
	// Enabled flag gates the feature independently of ContextWindow.
	Compaction compaction.CompactionSettings
	// SummaryStream produces the provider stream used to generate compaction
	// summaries. Defaults to Stream when nil.
	SummaryStream provider.StreamFn
	// SummaryModel is the model used for summarization. When zero, a model is
	// synthesized from Model/ContextWindow.
	SummaryModel provider.Model

	// Extra is forwarded to StreamConfig.Extra.
	Extra map[string]any
}

// streamAssistantResponse runs one assistant turn: it builds the request from
// agentCtx, streams the provider response, back-fills the partial into
// agentCtx.Messages, and returns the final assistant message. The sequence
// (transformContext → convertToLlm → resolve key → stream → drain) is kept
// identical to pi. It never returns an error for a request failure — such
// failures arrive as a terminal assistant message with stopReason error/aborted.
func streamAssistantResponse(ctx context.Context, agentCtx *agentcore.AgentContext, cfg LoopConfig, emit agentcore.EmitFunc) (agentcore.AssistantMessage, error) {
	// 1. transformContext (optional, must not error).
	msgs := agentCtx.Messages
	if cfg.TransformContext != nil {
		msgs = cfg.TransformContext(ctx, msgs)
	}
	// Last-mile DLP: even if an ingestion seam was missed (a resumed session, a
	// caller-built context, a compaction the runtime did not see), nothing
	// credential-shaped reaches the provider. Clean text costs one linear scan.
	if r := redact.Default(); r != nil {
		msgs = r.Messages(redact.SeamUserPrompt, msgs)
	}
	// 2. convertToLlm (filter UI-only; default identity).
	if cfg.ConvertToLlm != nil {
		msgs = cfg.ConvertToLlm(msgs)
	}
	// 3. shape the LLM context.
	systemPrompt := agentCtx.SystemPrompt
	if r := redact.Default(); r != nil {
		systemPrompt = r.Text(redact.SeamSession, "", systemPrompt)
	}
	llm := provider.LlmContext{
		SystemPrompt: systemPrompt,
		Messages:     msgs,
		Tools:        agentCtx.Tools,
	}
	// 4. resolve API key dynamically, fall back to static.
	key := cfg.APIKey
	if cfg.GetAPIKey != nil {
		if dyn := cfg.GetAPIKey(ctx, cfg.Provider); dyn != "" {
			key = dyn
		}
	}
	// 5. build the provider stream.
	stream, err := cfg.Stream(ctx, cfg.Model, llm, provider.StreamConfig{
		APIKey:        key,
		ThinkingLevel: cfg.ThinkingLevel,
		Extra:         cfg.Extra,
	})
	if err != nil {
		// Early "cannot build stream" failure: synthesize a terminal message so
		// the loop has a uniform assistant message to record. A cancelled ctx
		// during the build is reported as aborted, like every other cancellation.
		return terminalMessage(cfg, err), nil
	}

	// 6. drain the stream, back-filling the partial into the context.
	addedPartial := false
	backfill := func(partial agentcore.AssistantMessage) {
		if !addedPartial {
			agentCtx.Messages = append(agentCtx.Messages, partial)
			addedPartial = true
		} else {
			agentCtx.Messages[len(agentCtx.Messages)-1] = partial
		}
	}

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case provider.StreamStartEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageStartEvent{Message: e.Partial}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamTextEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageUpdateEvent{Message: e.Partial, AssistantMessageEvent: e}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamThinkingEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageUpdateEvent{Message: e.Partial, AssistantMessageEvent: e}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamToolCallEvent:
			backfill(e.Partial)
			if err := emit(ctx, agentcore.MessageUpdateEvent{Message: e.Partial, AssistantMessageEvent: e}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamNoticeEvent:
			// Provider-layer progress (a cut stream being retried) is an observer
			// event, not message content: it must not create or mutate the
			// assistant partial, and a front-end that ignores it loses nothing.
			if err := emit(ctx, agentcore.ProviderNoticeEvent{
				Kind:        e.Kind,
				Provider:    e.Provider,
				Reason:      e.Reason,
				Window:      e.Window,
				Attempt:     e.Attempt,
				MaxAttempts: e.MaxAttempts,
				Detail:      e.Detail,
			}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
		case provider.StreamDoneEvent:
			final := finalizeMessage(agentCtx, e.Message, &addedPartial)
			if err := emit(ctx, agentcore.MessageEndEvent{Message: final}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
			return final, nil
		case provider.StreamErrorEvent:
			final := finalizeMessage(agentCtx, e.Message, &addedPartial)
			if err := emit(ctx, agentcore.MessageEndEvent{Message: final}); err != nil {
				return agentcore.AssistantMessage{}, err
			}
			return final, nil
		}
	}

	// 7. stream ended without done/error: fall back to the stream result.
	final, resErr := stream.Result(ctx)
	if resErr != nil {
		return terminalMessage(cfg, resErr), nil
	}
	final = finalizeMessage(agentCtx, final, &addedPartial)
	if err := emit(ctx, agentcore.MessageEndEvent{Message: final}); err != nil {
		return agentcore.AssistantMessage{}, err
	}
	return final, nil
}

// finalizeMessage replaces the placeholder partial with the final message, or
// appends it if the provider sent done/error without a prior start. The stored
// copy is redacted: an assistant can echo a credential it read before the
// redactor was enabled, and that echo must not enter the next request.
func finalizeMessage(agentCtx *agentcore.AgentContext, final agentcore.AssistantMessage, addedPartial *bool) agentcore.AssistantMessage {
	if r := redact.Default(); r != nil {
		if m, ok := r.Message(redact.SeamAssistant, final).(agentcore.AssistantMessage); ok {
			final = m
		}
	}
	if *addedPartial {
		agentCtx.Messages[len(agentCtx.Messages)-1] = final
	} else {
		agentCtx.Messages = append(agentCtx.Messages, final)
		*addedPartial = true
	}
	return final
}

// terminalMessage builds the terminal assistant message for a failure that
// never produced a usable provider stream. A cancellation is filed as ABORTED
// rather than error: interrupting is the user's own action, and calling it an
// error is what made every front-end print "error: context canceled" for a
// keystroke — the wording that made Ctrl+C read as a malfunction. Everything
// else stays an error.
func terminalMessage(cfg LoopConfig, err error) agentcore.AssistantMessage {
	msg := agentcore.AssistantMessage{
		RoleField:    agentcore.RoleAssistant,
		Model:        cfg.Model,
		Provider:     cfg.Provider,
		StopReason:   agentcore.StopReasonError,
		ErrorMessage: err.Error(),
	}
	if errors.Is(err, context.Canceled) {
		msg.StopReason = agentcore.StopReasonAborted
		// No message: the front-end prints its own interrupt notice for this
		// stop reason, and "context canceled" is the machinery talking.
		msg.ErrorMessage = ""
	}
	return msg
}
