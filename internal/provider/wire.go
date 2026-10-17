package provider

import "strings"

// Wire is one API dialect. The values are the wires golder speaks today
// (Responses, Messages, Chat Completions) or has a selector for (Gemini
// v1beta). The constant order is also the selection priority: a model that a
// provider can serve more than one way is reached the richest way first. The
// relative order is fixed — responses > messages > v1beta > chat — and a
// provider may serve any subset of it.
type Wire string

const (
	WireResponses Wire = "responses"
	WireMessages  Wire = "messages"
	WireGemini    Wire = "v1beta"
	WireChat      Wire = "chat"
)

// WirePriority is the fixed preference order.
var WirePriority = []Wire{WireResponses, WireMessages, WireGemini, WireChat}

// protocolForWire maps a wire onto the internal protocol selector the resolver
// switches on. ok=false means the wire has no driver yet.
func protocolForWire(w Wire) (string, bool) {
	switch w {
	case WireResponses:
		return ProtocolOpenAIResponses, true
	case WireMessages:
		return ProtocolAnthropic, true
	case WireGemini:
		return ProtocolGemini, true
	case WireChat:
		return ProtocolOpenAI, true
	}
	return "", false
}

// wireForProtocol is the inverse; unknown selectors yield "".
func wireForProtocol(p string) Wire {
	switch p {
	case ProtocolOpenAIResponses:
		return WireResponses
	case ProtocolAnthropic:
		return WireMessages
	case ProtocolGemini:
		return WireGemini
	case ProtocolOpenAI:
		return WireChat
	}
	return ""
}

// ModelWire is the model's native wire — the dialect its vendor publishes it on
// — independent of who serves it. It is deliberately a naming heuristic on the
// model id (the signal opencode itself uses): GPT, Codex, o-series, Grok and
// Muse are announced on the Responses API; Claude on Messages; Gemini on the
// Gemini v1beta API. The domestic families (DeepSeek, GLM, Kimi, Qwen, MiniMax,
// MiMo, LongCat, Step, Hy) all publish a Responses endpoint today — DeepSeek
// first, the rest following — so they are Responses-native too; what a *given
// gateway* actually serves is settled by the lazy probe, not by guessing here.
// Anything else gets Chat, the dialect every OpenAI-compatible server speaks.
func ModelWire(model string) Wire {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return WireChat
	}
	switch {
	case strings.Contains(m, "claude"):
		return WireMessages
	case strings.Contains(m, "gemini"):
		return WireGemini
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"),
		strings.Contains(m, "muse"), strings.Contains(m, "grok"):
		return WireResponses
	case strings.Contains(m, "deepseek"), strings.Contains(m, "glm"),
		strings.Contains(m, "kimi"), strings.Contains(m, "qwen"),
		strings.Contains(m, "minimax"), strings.Contains(m, "mimo"),
		strings.Contains(m, "longcat"), strings.Contains(m, "step-"),
		strings.Contains(m, "hy3"), strings.Contains(m, "hy4"):
		return WireResponses
	}
	if len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9' {
		return WireResponses
	}
	return WireChat
}

// ProviderWires is the set of wires a provider serves: its registered protocol,
// every declared wire variant, and — for an OpenAI-wired provider — the
// Responses upgrade every such gateway is assumed to serve (the same assumption
// AllowsProtocol makes). The result is in WirePriority order. It is a
// capability list, not a rule about models; SelectWire decides which one a run
// uses.
func ProviderWires(spec ProviderSpec) []Wire {
	served := map[Wire]bool{}
	if w := wireForProtocol(spec.Protocol); w != "" {
		served[w] = true
	}
	if spec.Protocol == ProtocolOpenAI {
		served[WireResponses] = true
	}
	for _, v := range spec.WireVariants {
		if w := wireForProtocol(v.Protocol); w != "" {
			served[w] = true
		}
	}
	out := make([]Wire, 0, len(served))
	for _, w := range WirePriority {
		if served[w] {
			out = append(out, w)
		}
	}
	return out
}

// SelectWire picks the wire a run speaks:
//
//  1. the model's native wire, when the provider serves it;
//  2. otherwise the highest-priority wire the provider serves — a gateway that
//     offers only Chat reaches a Responses-native model over Chat, which is
//     exactly what translation gateways do.
//
// An explicit --protocol is not handled here: the resolver validates it with
// AllowsProtocol and keeps the user's choice verbatim.
func SelectWire(spec ProviderSpec, model string) Wire {
	wires := ProviderWires(spec)
	if len(wires) == 0 {
		return WireChat
	}
	if native := ModelWire(model); containsWire(wires, native) {
		return native
	}
	if registered := wireForProtocol(spec.Protocol); registered != "" {
		return registered
	}
	return wires[0]
}

// WireNative reports whether the provider serves the model's native wire, which
// is the condition that makes SelectWire's choice unambiguous.
func WireNative(spec ProviderSpec, model string) bool {
	return containsWire(ProviderWires(spec), ModelWire(model))
}

// IsDecisionModel reports whether a model id names a decision model — TypeSafe's
// System One (jev / systemone) — rather than a conversation model. Decision
// models answer typed questions and share nothing with the chat wires, so the
// run path refuses them; the judge reaches them through its own client. Some
// gateways list one beside their chat models, which is why the check is on the
// model, not on the provider.
func IsDecisionModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "jev") || strings.HasPrefix(m, "systemone")
}

// ModelFamily groups model ids a vendor ships as one line: "deepseek-v4.1-flash"
// and "deepseek-v4-pro" are both the deepseek family. The probe uses it so one
// confirmed model answers for its siblings — a gateway routes a family the same
// way, and the first model asked pays for the answer. A routed id
// ("google/gemini-…") is grouped by its model half.
func ModelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	end := 0
	for end < len(m) {
		c := m[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return ""
	}
	return m[:end]
}

func containsWire(wires []Wire, w Wire) bool {
	for _, have := range wires {
		if have == w {
			return true
		}
	}
	return false
}
