package provider

// Structural diagnostics only. No prompts, model/provider identifiers, URLs,
// reasoning/signatures, call IDs, arguments, session IDs or upstream messages.
import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/diagtrace"
	"github.com/openai/openai-go"
)

var responsesTraceSequence atomic.Uint64

type responsesTraceID uint64

func nextResponsesTrace() responsesTraceID {
	if !diagtrace.Enabled() {
		return 0
	}
	return responsesTraceID(responsesTraceSequence.Add(1))
}

func (id responsesTraceID) event(format string, args ...any) {
	if id != 0 {
		diagtrace.Event("responses", "request=%d "+format, append([]any{uint64(id)}, args...)...)
	}
}

func (id responsesTraceID) result(err error, empty bool) {
	if id == 0 {
		return
	}
	class, status := diagtrace.ErrorClass(err), 0
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
		class = "http"
	}
	if err != nil {
		// Inspect locally, but never record the error or any extracted text.
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "must be passed back") && (strings.Contains(text, "thinking") || strings.Contains(text, "reasoning")) {
			class = "thinking_replay_rejected"
		}
	}
	id.event("request.end failed=%t error_class=%s http_status=%d empty=%t", err != nil, class, status, empty)
}

func (id responsesTraceID) replay(step int, msg agentcore.AssistantMessage, items []json.RawMessage, calls []agentcore.ToolCallContent, providerMatch, scopeMatch, thinkingOn, placeholder bool) {
	if id == 0 {
		return
	}
	disposition := "replayed"
	switch {
	case len(msg.ReasoningItems) == 0:
		disposition = "not_recorded"
	case !providerMatch:
		disposition = "provider_mismatch"
	case !scopeMatch:
		disposition = "model_mismatch"
	case len(items) == 0:
		disposition = "no_valid_items"
	}
	source := "requested"
	if msg.Model == "" {
		source = "legacy_response"
		if msg.ResponseModel == "" {
			source = "missing"
		}
	}
	local := 0
	for _, call := range calls {
		if !call.IsServer() {
			local++
		}
	}
	id.event("replay step=%d stored=%d selected=%d disposition=%s model_source=%s thinking_on=%t head_nonempty=%t placeholder=%t local_calls=%d server_calls=%d",
		step, len(msg.ReasoningItems), len(items), disposition, source, thinkingOn, reasoningHasText(items), placeholder, local, len(calls)-local)
}

func (id responsesTraceID) capture(items []json.RawMessage, terminal, finalized int, completed, alias bool) {
	if id == 0 {
		return
	}
	reasoningParts, thinkingParts, otherParts := 0, 0, 0
	for _, raw := range items {
		var item struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) == nil {
			for _, part := range item.Content {
				switch part.Type {
				case "reasoning_text":
					reasoningParts++
				case "thinking":
					thinkingParts++
				default:
					otherParts++
				}
			}
		}
	}
	id.event("capture completed=%t finalized=%d terminal=%d stored=%d head_nonempty=%t reasoning_text_parts=%d thinking_parts=%d other_parts=%d model_alias=%t",
		completed, finalized, terminal, len(items), reasoningHasText(items), reasoningParts, thinkingParts, otherParts, alias)
}
