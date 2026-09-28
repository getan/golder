package provider

// Protocol normalization (US-001, #538). The user-facing --protocol / protocol
// value accepts three OpenAI wire variants in addition to anthropic:
//
//	openai           → Chat Completions (POST {base_url}/chat/completions)
//	openai/chat      → Chat Completions (alias of "openai")
//	openai/resp_api  → Responses API    (POST {base_url}/responses)
//	anthropic        → Anthropic Messages
//	""               → unset; downstream falls back to model-id heuristics
//
// NormalizeProtocol collapses these into a small set of canonical internal
// selectors so ResolveProvider (#543) can switch on chat vs resp_api without
// re-parsing surface syntax. "openai" and "openai/chat" both normalize to
// ProtocolOpenAI, keeping the existing Chat Completions path byte-for-byte
// unchanged; only "openai/resp_api" produces the new selector.

import (
	"fmt"
	"strings"
)

// ProtocolOpenAIResponses is the canonical selector for the OpenAI Responses
// API wire format (POST {base_url}/responses). It is distinct from
// ProtocolOpenAI (Chat Completions) so ResolveProvider can route to the
// SDK-based Responses driver.
const ProtocolOpenAIResponses = "openai/resp_api"

// responsesFamilySubstrings are model-id substrings (matched case-insensitively)
// whose models prefer the Responses wire protocol when the provider serves both
// OpenAI wire variants. "muse" covers the muse-spark series (not Claude). The
// o-series reasoning models (o1/o3/o4-mini) carry no "gpt" marker and are
// handled by the leading-letter rule in PreferResponses.
var responsesFamilySubstrings = []string{"grok", "muse", "deepseek", "gpt"}

// PreferResponses reports whether modelID belongs to a Responses-preferred
// family. It is a pure naming heuristic (mirroring opencode's own
// shouldUseResponsesApi): an explicit --protocol always wins over it, and it
// never applies to Anthropic-wired providers (ResolveNamedProvider guards
// that), so a Claude id can never be routed to the Responses driver here.
func PreferResponses(modelID string) bool {
	m := strings.ToLower(strings.TrimSpace(modelID))
	if m == "" {
		return false
	}
	for _, sub := range responsesFamilySubstrings {
		if strings.Contains(m, sub) {
			return true
		}
	}
	// o-series reasoning models: o1, o3, o4-mini, ...
	if len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9' {
		return true
	}
	return false
}

// NormalizeProtocol maps a raw --protocol / protocol value to a canonical
// internal selector. Input is trimmed and lower-cased before matching. An empty
// value stays empty (unset → model-id heuristics). Recognized values normalize
// to ProtocolOpenAI, ProtocolOpenAIResponses, or ProtocolAnthropic. Any other
// value is an error naming the accepted set, so a typo surfaces to the caller
// for exit-code mapping instead of silently falling through.
func NormalizeProtocol(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case ProtocolOpenAI, "openai/chat":
		return ProtocolOpenAI, nil
	case ProtocolOpenAIResponses:
		return ProtocolOpenAIResponses, nil
	case ProtocolAnthropic:
		return ProtocolAnthropic, nil
	default:
		return "", fmt.Errorf("unknown --protocol %q (want openai|openai/chat|openai/resp_api|anthropic)", raw)
	}
}

// ProtocolLabel maps a raw --protocol value to the human-facing label shown in
// the startup banner's Protocol row, so the displayed wire format matches what
// pigo actually speaks. It differs from NormalizeProtocol in one deliberate way:
// the bare "openai" input is surfaced as "openai/chat", making the Chat
// Completions variant explicit rather than ambiguous. "openai/resp_api" and
// "anthropic" pass through as themselves.
//
// An empty input returns empty (the banner then falls back to "—" or the
// provider name, so an unset protocol on a named/inferred provider is not
// mislabeled). An unrecognized value returns the trimmed input verbatim — the
// label is presentation-only and must never fail; a real typo is already
// rejected upstream by NormalizeProtocol during resolution.
func ProtocolLabel(raw string) string {
	canonical, err := NormalizeProtocol(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	switch canonical {
	case ProtocolOpenAI:
		return "openai/chat"
	case ProtocolOpenAIResponses:
		return ProtocolOpenAIResponses
	case ProtocolAnthropic:
		return ProtocolAnthropic
	default:
		return ""
	}
}
