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

// ProtocolGemini is the selector for the Gemini v1beta wire. Selection knows
// the wire (see ModelWire), but the driver does not exist yet, so a run that
// selects it reports that plainly instead of silently using another wire.
const ProtocolGemini = "gemini"

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
	case ProtocolGemini:
		return ProtocolGemini, nil
	default:
		return "", fmt.Errorf("unknown --protocol %q (want openai|openai/chat|openai/resp_api|anthropic|gemini)", raw)
	}
}

// EffectiveProtocol reports the wire protocol a run will actually speak for
// the given provider selection, model id, and explicit --protocol flag. It
// mirrors ResolveNamedProvider's effective-protocol computation without its
// driver construction or errors, so the startup banner can display the real
// wire format (a multi-protocol gateway such as opencode-go shows
// openai/resp_api for a deepseek model and openai/chat for anything else).
// An explicit protocol wins when it is valid and compatible with the provider;
// otherwise the wire comes from SelectWire — the model's native wire when the
// provider serves it, the provider's registered wire otherwise. Unknown
// providers, unknown protocols, and incompatible pairs yield "" so the caller
// falls back to its placeholder instead of displaying a misleading value.
func EffectiveProtocol(providerName, model, protocol string) string {
	spec, ok := LookupProviderSpec(strings.TrimSpace(providerName))
	if !ok {
		if name, inferred := InferProviderFromModel(model); inferred {
			spec, ok = LookupProviderSpec(name)
		}
		if !ok {
			if canonical, err := NormalizeProtocol(protocol); err == nil {
				return canonical
			}
			return ""
		}
	}
	if strings.TrimSpace(protocol) != "" {
		canonical, err := NormalizeProtocol(protocol)
		if err != nil {
			return ""
		}
		if !AllowsProtocol(spec, canonical) {
			return ""
		}
		return canonical
	}
	// The lazy probe may already have confirmed the endpoint's real wire for
	// this model; prefer that answer so the banner and the run agree.
	wire := SelectWire(spec, model)
	if _, pinned := staticWire(spec, model); !pinned {
		if confirmed, ok := CachedWire(spec.Name, model); ok && wireAllowed(spec, confirmed) {
			wire = confirmed
		}
	}
	if canonical, ok := protocolForWire(wire); ok {
		return canonical
	}
	return spec.Protocol
}

// AllowsProtocol reports whether an explicit protocol selection is legal for a
// provider: its own registered Protocol, the Responses upgrade every
// OpenAI-wired gateway is assumed to serve, or a wire variant the spec declares
// (see ProviderSpec.WireVariants). It is the single rule behind both the
// resolver's conflict error and the display path's protocol label, so what
// `/provider` and the banner call a gateway's protocol and what a run can
// actually select cannot disagree.
func AllowsProtocol(spec ProviderSpec, canonical string) bool {
	if canonical == "" {
		return false
	}
	if canonical == spec.Protocol {
		return true
	}
	if spec.Protocol == ProtocolOpenAI && canonical == ProtocolOpenAIResponses {
		return true
	}
	for _, v := range spec.WireVariants {
		if v.Protocol == canonical {
			return true
		}
	}
	return false
}

// wireVariantFor returns the declared wire variant matching canonical, so the
// resolver can take that wire's auth scheme.
func wireVariantFor(spec ProviderSpec, canonical string) (WireVariant, bool) {
	for _, v := range spec.WireVariants {
		if v.Protocol == canonical {
			return v, true
		}
	}
	return WireVariant{}, false
}

// ProtocolChoices renders the protocol values an explicit --protocol may name
// for this provider — quoted, comma-separated — for a conflict error: the
// registered protocol, the Responses upgrade when it applies, and every declared
// wire variant.
func ProtocolChoices(spec ProviderSpec) string {
	choices := []string{spec.Protocol}
	if spec.Protocol == ProtocolOpenAI {
		choices = append(choices, ProtocolOpenAIResponses)
	}
	for _, v := range spec.WireVariants {
		if v.Protocol != spec.Protocol {
			choices = append(choices, v.Protocol)
		}
	}
	quoted := make([]string, 0, len(choices))
	for _, c := range choices {
		quoted = append(quoted, fmt.Sprintf("%q", c))
	}
	return strings.Join(quoted, ", ")
}

// ProtocolLabel maps a raw --protocol value to the human-facing label shown in
// the startup banner's Protocol row, so the displayed wire format matches what
// golder actually speaks. It differs from NormalizeProtocol in one deliberate way:
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
	case ProtocolGemini:
		return ProtocolGemini
	default:
		return ""
	}
}
