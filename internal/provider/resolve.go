package provider

// Provider resolution moved here from cmd/golder (US-004, #361): mapping a model
// id / --provider / --protocol selection to a concrete wire driver, plus the
// base-url override precedence. Environment lookups are injected as an
// env func(string) string so callers (and tests) control the environment
// instead of reaching into the process env directly.

import (
	"fmt"
	"strings"

	"github.com/getan/golder/internal/cli/config"
)

// ResolveProvider maps a model id to a built-in provider. An explicit
// --provider name wins over every other rule: it selects a built-in provider
// from the registry and constructs the matching wire driver (see
// ResolveNamedProvider). When provider is empty, protocol and model-id
// heuristics apply as before.
//
// When protocol is a non-empty explicit selection ("openai" or "anthropic") it
// wins over the model-id heuristics: the provider is built directly for that
// wire format against baseURL, which is how a user points golder at a self-hosted
// or third-party endpoint and says which protocol it speaks. An "anthropic"
// selection with no baseURL targets the public Anthropic API.
//
// When protocol is empty, resolution falls back to model-id heuristics:
//
//  1. A bare provider name (e.g. "zai") is canonicalized to that provider's
//     DefaultModel (see CanonicalizeModel, issue #564).
//  2. An "ollama/" prefix (or a base URL on the Ollama port) → local Ollama.
//  3. An "nvidia/" prefix → NVIDIA NIM (strips the prefix for the wire id).
//  4. Model-name inference: with no --base-url, a well-known model-name prefix
//     (e.g. "claude-*", "deepseek-*") selects its first-party built-in provider
//     via ResolveNamedProvider (see InferProviderFromModel).
//  5. A bare provider name with no DefaultModel is an error naming the
//     mismatch, rather than a silent OpenRouter fallback that fails later with
//     a misleading missing-API-key error.
//  6. Everything else → OpenRouter, the reference OpenAI-compatible gateway.
//
// An unknown protocol value is an error, surfaced to the caller for exit-code
// mapping rather than silently falling back.
func ResolveProvider(model, baseURL, protocol, providerName string, env func(string) string) (Provider, string, error) {
	// Explicit --provider selects a built-in provider from the registry and
	// wins over both --protocol inference and model-id heuristics.
	if strings.TrimSpace(providerName) != "" {
		return ResolveNamedProvider(providerName, model, baseURL, protocol, env)
	}

	// 0. Explicit protocol selection wins over every heuristic. Normalize the
	// surface value first so "openai" and "openai/chat" collapse to the same
	// Chat Completions selector and "openai/resp_api" routes to the Responses
	// driver; an unknown value surfaces as an error for exit-code mapping.
	canonical, err := NormalizeProtocol(protocol)
	if err != nil {
		return nil, "", err
	}
	switch canonical {
	case ProtocolOpenAI:
		if strings.TrimSpace(baseURL) == "" {
			return nil, "", fmt.Errorf("--protocol openai requires --base-url")
		}
		return NewOpenAICompatibleProvider(baseURL, []Model{{Provider: "openai", ID: model, SupportsImages: true}}), "openai", nil
	case ProtocolOpenAIResponses:
		// The Responses driver has no public default endpoint here: unlike the
		// anthropic path (which targets the public API), resp_api mirrors the
		// Chat Completions requirement and demands an explicit --base-url.
		if strings.TrimSpace(baseURL) == "" {
			return nil, "", fmt.Errorf("--protocol openai/resp_api requires --base-url")
		}
		return NewOpenAIResponsesProvider("openai", baseURL, []Model{{Provider: "openai", ID: model, SupportsImages: true}}), "openai", nil
	case ProtocolAnthropic:
		return NewAnthropicProvider(baseURL, []Model{{Provider: "anthropic", ID: model, SupportsImages: true}}), "anthropic", nil
	case "":
		// fall through to heuristic resolution
	}

	// 0.5 A bare provider name (e.g. "zai", "deepseek") is a common shorthand for
	//     "that provider's default model" (issue #564): /model zai and --model zai
	//     previously fell through to OpenRouter with the literal id "zai", failing
	//     there with a misleading "openrouter: missing API key". Substitute the
	//     provider's DefaultModel (registry.go) so both the driver and the wire
	//     request carry a real model id. Any other id is returned unchanged.
	if canon := CanonicalizeModel(model); canon != model {
		model = canon
	}

	// 1. Local Ollama by prefix or port.
	if strings.HasPrefix(model, "ollama/") || strings.Contains(baseURL, "11434") {
		id := strings.TrimPrefix(model, "ollama/")
		return NewOllamaProvider(baseURL, []Model{{Provider: "ollama", ID: id, SupportsImages: true}}), "ollama", nil
	}
	// 2. NVIDIA NIM by prefix.
	if strings.HasPrefix(model, "nvidia/") {
		id := strings.TrimPrefix(model, "nvidia/")
		return NewNvidiaProvider(baseURL, []Model{{Provider: "nvidia", ID: id, SupportsImages: true}}), "nvidia", nil
	}
	// 3. Model-name inference: with no --provider/--protocol (both empty here) and
	//    no --base-url, guess the provider from the model name's well-known prefix
	//    (e.g. "claude-*" → anthropic, "deepseek-*" → deepseek). A confident hit is
	//    routed through ResolveNamedProvider so the provider's registry protocol,
	//    default base URL, and API-key env var are used. A --base-url is treated as
	//    a custom-endpoint signal that should not be second-guessed, so inference is
	//    skipped when one is given. Ambiguous/unknown names fall through to (4).
	if strings.TrimSpace(baseURL) == "" {
		if name, ok := InferProviderFromModel(model); ok {
			return ResolveNamedProvider(name, model, baseURL, protocol, env)
		}
	}
	// 4. Default: OpenRouter — but a bare built-in provider name that reached this
	//    point names a provider with no DefaultModel (e.g. a special-auth
	//    gateway). Silently routing it to OpenRouter would fail later with a
	//    confusing missing-API-key error, so surface the mismatch instead.
	if name := strings.ToLower(strings.TrimSpace(model)); IsProviderName(name) {
		return nil, "", fmt.Errorf("%q names a provider, not a model id; pass a model id (see /model) or select it with --provider %s", model, name)
	}
	return NewOpenRouterProvider(baseURL, []Model{{Provider: "openrouter", ID: model, SupportsImages: true}}), "openrouter", nil
}

// InferProviderFromModel guesses the built-in provider name that serves a given
// model id, based on the id's well-known name prefix (e.g. "claude-*" →
// anthropic, "deepseek-*" → deepseek). This is what lets `golder -m
// claude-opus-4-8` reach the Anthropic API without the user spelling out the
// provider or its wire protocol (ResolveProvider step 3).
//
// The mapping is deliberately conservative: only names that unambiguously
// identify a single built-in provider are inferred. Ambiguous families served
// by many gateways (notably "llama-*", "qwq-*", "gemma-*", "mixtral-*") are
// NOT inferred — they return ok=false so the caller falls through to its
// existing default (OpenRouter). The prefixes live in the registry
// (ProviderSpec.ModelPrefixes), in registry order; a test pins that no two
// providers claim overlapping prefixes, so the order only matters for
// determinism. Matching is case-insensitive.
func InferProviderFromModel(model string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	if id == "" {
		return "", false
	}
	// A "provider/model" style id (e.g. "openai/gpt-4o") is an OpenRouter-style
	// routed id, not a bare model name — leave those to the caller's default
	// handling rather than inferring from the leading segment.
	if strings.Contains(id, "/") {
		return "", false
	}
	for _, spec := range providerRegistry {
		for _, prefix := range spec.ModelPrefixes {
			if strings.HasPrefix(id, prefix) {
				return spec.Name, true
			}
		}
	}
	return "", false
}

// CanonicalizeModel maps a bare built-in provider name (e.g. "zai", "DEEPSEEK")
// to that provider's DefaultModel (e.g. "glm-4.7"), so a user who types the
// provider alone gets its default model instead of the literal id being sent to
// OpenRouter (issue #564). Any other id — a real model id, an unknown name, a
// "provider/model" routed id — is returned unchanged.
//
// The mapping is exact-match on the trimmed, lowercased id against the built-in
// provider registry; a model id that happens to equal a provider name is not a
// meaningful OpenRouter id, so the ambiguity is harmless.
func CanonicalizeModel(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return model
	}
	spec, ok := LookupProviderSpec(name)
	if !ok {
		return model
	}
	if spec.DefaultModel != "" {
		return spec.DefaultModel
	}
	return model
}

// IsProviderName reports whether id is exactly a built-in provider name
// (case-insensitive, surrounding whitespace tolerated).
func IsProviderName(id string) bool {
	name := strings.ToLower(strings.TrimSpace(id))
	if name == "" {
		return false
	}
	_, ok := LookupProviderSpec(name)
	return ok
}

// ResolveNamedProvider builds the driver for an explicit --provider selection.
// It looks the name up in the built-in registry and constructs the wire driver
// for the effective protocol: "openai" → an OpenAI-compatible (Bearer) driver,
// "anthropic" → an Anthropic-Messages driver, "openai/resp_api" → the Responses
// driver. An explicit --protocol wins; otherwise a Responses-family model id
// upgrades an OpenAI-wired provider (see PreferResponses). The base URL follows the override
// precedence in ResolveBaseURL (--base-url > provider-specific env > generic
// <PROVIDER>_BASE_URL > spec default). The returned provider-name string is the
// spec name, so downstream API-key resolution reads the provider's own env var
// (spec.EnvVars).
//
// Special providers with bespoke auth (azure/bedrock/vertex/cloudflare —
// AuthScheme aws/azure/special, or the cloudflare-* names) are routed to
// ResolveSpecialProvider, which validates their required env vars and composes
// the concrete endpoint (node #188).
func ResolveNamedProvider(name, model, baseURL, protocol string, env func(string) string) (Provider, string, error) {
	spec, ok := LookupProviderSpec(name)
	if !ok {
		return nil, "", fmt.Errorf("unknown --provider %q (available: %s)", name, strings.Join(ProviderNames(), ", "))
	}
	// A concurrently-set --protocol must be compatible with the provider's own
	// protocol (equal, or a resp_api upgrade on an OpenAI-wired provider);
	// an incompatible pair is a user error naming both flags. Normalize the raw
	// value first so aliases (e.g. "openai/chat" for an "openai" spec) don't
	// falsely conflict, and a genuine typo surfaces as a clear "unknown --protocol"
	// error rather than a misleading conflict message.
	// Effective wire protocol: an explicit --protocol wins; otherwise a
	// Responses-family model id upgrades an OpenAI-wired provider to the
	// Responses driver (family heuristic, see PreferResponses). Anthropic-wired
	// providers never take the heuristic path.
	effective := spec.Protocol
	if strings.TrimSpace(protocol) != "" {
		canonical, err := NormalizeProtocol(protocol)
		if err != nil {
			return nil, "", err
		}
		// An OpenAI-wired provider is assumed to serve both wire variants, so
		// an explicit resp_api selection is an upgrade, not a conflict.
		compatible := canonical == spec.Protocol ||
			(spec.Protocol == ProtocolOpenAI && canonical == ProtocolOpenAIResponses)
		if !compatible {
			return nil, "", fmt.Errorf("--provider %q speaks the %q protocol, which conflicts with --protocol %q; drop --protocol or set it to %q", name, spec.Protocol, protocol, spec.Protocol)
		}
		effective = canonical
	} else if spec.Protocol == ProtocolOpenAI && PreferResponses(model) {
		effective = ProtocolOpenAIResponses
	}
	// Special-auth providers (Azure / Bedrock / Vertex / Cloudflare) compose
	// their endpoint from several env vars and/or need non-standard credential
	// validation, so route them to the dedicated resolver (US-007 / node #188).
	// It performs its own base-URL composition (honoring the --base-url override)
	// and returns a clear error naming any absent required env var.
	if IsSpecialAuthProvider(spec) {
		p, err := ResolveSpecialProvider(spec, model, baseURL, env)
		if err != nil {
			return nil, "", err
		}
		return p, spec.Name, nil
	}
	// Base-URL precedence (US-004 / FR-8, FR-9): --base-url flag > provider-
	// specific base-url env var(s) > generic <PROVIDER>_BASE_URL > spec default.
	url := ResolveBaseURL(spec, baseURL, env)
	models := []Model{{Provider: spec.Name, ID: model, SupportsImages: true}}
	// Note: spec.ExtraHeaders would be attached here, but the exported generic
	// constructors do not yet accept custom headers; all built-in specs currently
	// carry no ExtraHeaders, so this is a no-op today (refined alongside #188).
	switch effective {
	case ProtocolAnthropic:
		// Auth header follows the spec's AuthScheme (x-api-key + anthropic-version
		// for anthropic/minimax/minimax-cn; Bearer for any anthropic-protocol
		// gateway that authenticates with a plain bearer token). The driver name is
		// the spec name so errors reference the selected provider.
		return NewAnthropicProtocolProvider(spec.Name, url, spec.AuthScheme, models), spec.Name, nil
	case ProtocolOpenAI:
		return NewOpenAICompatibleProviderAs(spec.Name, url, models), spec.Name, nil
	case ProtocolOpenAIResponses:
		return NewOpenAIResponsesProvider(spec.Name, url, models), spec.Name, nil
	default:
		// The registry only ever stores openai/openai-resp/anthropic; guard anyway
		// so an unexpected value is a clear error rather than a nil provider.
		return nil, "", fmt.Errorf("--provider %q has unsupported protocol %q", name, spec.Protocol)
	}
}

// ResolveBaseURL determines the effective base URL for a selected provider,
// applying the base_url override precedence (US-004 / FR-8, FR-9). The first
// non-empty source wins, in this order:
//
//  1. flagBaseURL — the explicit --base-url/-u flag (highest).
//  2. provider-specific base-url env var(s) from spec.BaseURLEnvVars, in the
//     order the registry declares them (e.g. AZURE_OPENAI_BASE_URL).
//  3. the generic <PROVIDER>_BASE_URL env var, where <PROVIDER> is the provider
//     name uppercased with '-' rewritten to '_' (e.g. zai-coding-cn →
//     ZAI_CODING_CN_BASE_URL).
//  4. spec.DefaultBaseURL — the registry default (lowest).
//
// Values are trimmed of surrounding whitespace before the non-empty check, so a
// whitespace-only env var does not shadow a lower-precedence source. Environment
// lookups go through the injected env func so callers control the environment.
func ResolveBaseURL(spec ProviderSpec, flagBaseURL string, env func(string) string) string {
	// 1. Explicit flag wins over every env-var convention.
	if v := strings.TrimSpace(flagBaseURL); v != "" {
		return v
	}
	// 2. Provider-specific override env vars, in registry precedence order.
	for _, name := range spec.BaseURLEnvVars {
		if v := strings.TrimSpace(env(name)); v != "" {
			return v
		}
	}
	// 3. Generic <PROVIDER>_BASE_URL convention.
	if envName := config.GenericBaseURLEnvVar(spec.Name); envName != "" {
		if v := strings.TrimSpace(env(envName)); v != "" {
			return v
		}
	}
	// 4. Registry default.
	return spec.DefaultBaseURL
}

// BaseURLEnvVars lists endpoint override variables in resolution order.
func BaseURLEnvVars(spec ProviderSpec) []string {
	names := append([]string(nil), spec.BaseURLEnvVars...)
	generic := config.GenericBaseURLEnvVar(spec.Name)
	for _, name := range names {
		if name == generic {
			return names
		}
	}
	if generic != "" {
		names = append(names, generic)
	}
	return names
}
