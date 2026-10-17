// This file defines the central provider registry (US-001): a single source of
// truth for every built-in provider — its name, the environment variables that
// carry its API key (in precedence order), its default base URL, wire protocol,
// auth scheme, extra headers, provider-specific base-URL override env vars, and
// its model-facing behavior (which model-id prefixes infer it, which models.dev
// entry describes it, and any reasoning-effort ladder quirks its gateway has).
//
// Adding or supporting a provider should be ONE entry in providerRegistry:
// transport metadata first, then the optional behavior fields. Anything a
// provider does not declare falls back to the shared defaults — ResolveProvider
// builds the driver from the spec, InferProviderFromModel reads ModelPrefixes,
// the reasoning catalog reads ModelsDevID, and WireReasoningEffort consults
// ReasoningLadders before the model-family table in reasoning.go. The only
// per-provider code left is special-auth composition (special_auth.go), which
// needs request signing rather than metadata.
//
// The registry is deliberately additive and self-contained: later nodes wire
// auth resolution (auth.go), the --provider flag (main.go), base_url overrides,
// and per-provider construction (providers.go) to READ from it. This node only
// introduces the data + a lookup, so it does not change existing behavior.
//
// Data source: the PRD "Technical Considerations" table
// (tasks/prd-provider-env-parity.md), derived from pi's env-api-keys.ts and the
// per-provider *.models.ts files.
//
// Security: the registry holds only env var NAMES, never secret values. Keys are
// resolved from the environment at request time (see auth.go) and never logged.
package provider

import (
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// ProviderSpec is the metadata describing one built-in provider. It is the
// single source of truth consumed by auth resolution, the --provider flag,
// base_url override handling, and per-provider wiring.
type ProviderSpec struct {
	// Name is the canonical provider name (e.g. "deepseek", "zai-coding-cn").
	Name string
	// EnvVars lists the environment variables checked (in precedence order) for
	// this provider's API key. The first non-empty value wins.
	EnvVars []string
	// DefaultBaseURL is the provider's default API endpoint. It may be a template
	// (containing placeholders like {region}) for providers whose endpoint is
	// composed from additional parameters (Bedrock, Vertex, Cloudflare, Azure).
	DefaultBaseURL string
	// Protocol is the wire protocol the provider speaks: "openai" (OpenAI Chat
	// Completions) or "anthropic" (Anthropic Messages).
	Protocol string
	// AuthScheme names how credentials are attached: "bearer", "x-api-key",
	// "aws", "azure", or "special".
	AuthScheme string
	// WireVariants are additional wire protocols this gateway is known to serve
	// beyond its registered Protocol, each with the auth scheme that wire takes
	// (a gateway may authenticate its Messages endpoint differently from its
	// OpenAI-family one). An explicit --protocol naming a variant is legal and
	// builds the matching driver under this provider's own name, so per-provider
	// request decoration (the opencode session header, for one) still applies.
	// The registered Protocol stays the default: nothing changes for a user who
	// does not ask. Declare a variant only for a gateway whose other wire has
	// been verified — the registry's word is what turns "explicitly requested"
	// into "known to exist".
	WireVariants []WireVariant
	// Gateway marks a provider that serves several wires for its models (a
	// curated gateway such as opencode-go), as opposed to a first-party API that
	// publishes each model on one wire. Only a gateway is asked the lazy
	// "which wire do you actually serve for this model" question: for anyone
	// else the model's native wire and the provider's registered wire already
	// agree, and a probe would spend a request to learn nothing.
	Gateway bool
	// ExtraHeaders are provider-specific headers attached to every request (may
	// be nil).
	ExtraHeaders map[string]string
	// BaseURLEnvVars lists provider-specific base-URL override environment
	// variables (e.g. AZURE_OPENAI_BASE_URL), in precedence order. May be empty;
	// the generic <PROVIDER>_BASE_URL convention is handled by callers.
	BaseURLEnvVars []string
	// ForceProxy is the legacy default selection before the user saves /proxy
	// settings. An explicit provider list replaces these defaults completely.
	ForceProxy bool
	// DecisionOnly marks an endpoint that is not a conversation model: it answers
	// the reviewer's typed questions (TypeSafe's System One) and nothing else. It
	// is listed apart from the chat providers and refused as a conversation
	// target, because selecting it would silently break every turn — the wire it
	// speaks is a decision API, not chat completions. It still appears in the
	// registry so its requests follow the same proxy routing as everything else
	// and its key is discovered by the usual credential resolution.
	DecisionOnly bool

	// ModelsDevID is this provider's key in models.dev's api.json (used by the
	// reasoning ladder catalog). Empty means Name.
	ModelsDevID string

	// ModelPrefixes lists the lowercase model-id prefixes that identify this
	// provider when only --model is given (see InferProviderFromModel). Keep
	// entries precise: a prefix that another provider's ids also carry would
	// shadow that provider, and registry order decides ties.
	ModelPrefixes []string

	// ReasoningLadders overrides the shared model-family ladder table
	// (reasoning.go) for this provider's models: the first entry whose Match is
	// a case-insensitive substring of the model id wins, before the family
	// table is consulted. Declare a ladder here only when the gateway diverges
	// from the model family's usual ladder (e.g. a gateway that rejects a level
	// the upstream API accepts). Empty means "no quirks".
	ReasoningLadders []ReasoningLadder

	// DefaultModel is the model id a bare provider name resolves to (e.g.
	// "zai" → "glm-4.7", see CanonicalizeModel), so /model <provider> and
	// --model <provider> select a real wire id instead of sending the literal
	// provider name. Empty means the provider has no default; a bare name is
	// then reported as a provider/model mismatch instead of guessing.
	DefaultModel string
}

// WireVariant is one extra wire a gateway serves besides its registered
// Protocol: the protocol selector an explicit --protocol may name, and the auth
// scheme that wire expects.
type WireVariant struct {
	Protocol   string
	AuthScheme string
}

// ReasoningLadder is one model-family reasoning-effort rule: Match is a
// case-insensitive substring of the model id, Ladder the effort levels the
// gateway accepts for it, lowest to highest.
type ReasoningLadder struct {
	Match  string
	Ladder []agentcore.ThinkingLevel
}

// Protocol values.
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
)

// AuthScheme values.
const (
	AuthBearer  = "bearer"
	AuthXAPIKey = "x-api-key"
	AuthAWS     = "aws"
	AuthAzure   = "azure"
	AuthSpecial = "special"
)

// providerRegistry is the ordered list of all built-in provider specs. Order is
// stable so callers that enumerate providers (e.g. --help) get a deterministic
// list. LookupProviderSpec indexes it by name.
var providerRegistry = []ProviderSpec{
	{
		Name:           "anthropic",
		EnvVars:        []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
		DefaultBaseURL: anthropicBaseURL, // https://api.anthropic.com/v1
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
		ModelPrefixes:  []string{"claude-", "fable-"},
		DefaultModel:   "claude-fable-5-1",
	},
	{
		Name:           "openai",
		EnvVars:        []string{"OPENAI_API_KEY"},
		DefaultBaseURL: "https://api.openai.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"gpt-", "o1-", "o3-", "o4-"},
		DefaultModel:   "gpt-6-astra",
	},
	{
		Name:           "deepseek",
		EnvVars:        []string{"DEEPSEEK_API_KEY"},
		DefaultBaseURL: "https://api.deepseek.com",
		Protocol:       ProtocolOpenAIResponses,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"deepseek-"},
		DefaultModel:   "deepseek-v4-flash",
	},
	{
		Name:           "google",
		EnvVars:        []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"gemini-"},
		DefaultModel:   "gemini-3.8-flash",
	},
	{
		Name:           "groq",
		EnvVars:        []string{"GROQ_API_KEY"},
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		DefaultModel:   "llama-3.3-70b-versatile",
	},
	{
		Name:           "cerebras",
		EnvVars:        []string{"CEREBRAS_API_KEY"},
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		DefaultModel:   "gpt-oss-120b",
	},
	{
		Name:           "xai",
		EnvVars:        []string{"XAI_API_KEY"},
		DefaultBaseURL: "https://api.x.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"grok-"},
		DefaultModel:   "grok-4.6",
	},
	{
		Name:           "openrouter",
		EnvVars:        []string{"OPENROUTER_API_KEY"},
		DefaultBaseURL: openRouterBaseURL, // https://openrouter.ai/api/v1
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		Gateway:        true,
		DefaultModel:   "openai/gpt-4o",
	},
	{
		// TypeSafe's System One model: the decision-model reviewer's endpoint.
		// It speaks its own typed-decision API rather than chat completions,
		// which is what DecisionOnly says — /provider lists it apart from the
		// chat providers and refuses to switch to it, while /proxy can still
		// route it exactly like any other provider.
		Name:           "typesafe",
		DecisionOnly:   true,
		EnvVars:        []string{"TYPESAFE_API_KEY"},
		DefaultBaseURL: "https://api.typesafe.ai/v1/systemone",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		// TypeSafe refuses whole regions with HTTP 451 (measured: direct
		// requests from this machine are blocked, the same request through the
		// configured proxy answers 200), so a fresh install that has a proxy
		// address should route there by default rather than failing every
		// gated call. An explicit /proxy selection still wins — ForceProxy is
		// only the default before the user has chosen.
		ForceProxy: true,
	},
	{
		Name:           "zai",
		EnvVars:        []string{"ZAI_API_KEY"},
		DefaultBaseURL: "https://api.z.ai/api/coding/paas/v4",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"glm-"},
		DefaultModel:   "glm-4.7",
	},
	{
		Name:           "mistral",
		EnvVars:        []string{"MISTRAL_API_KEY"},
		DefaultBaseURL: "https://api.mistral.ai",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"mistral-", "codestral-", "devstral-"},
		DefaultModel:   "mistral-large-latest",
	},
	{
		Name:           "minimax",
		EnvVars:        []string{"MINIMAX_API_KEY"},
		DefaultBaseURL: "https://api.minimax.io/anthropic",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
		ModelPrefixes:  []string{"minimax-"},
		DefaultModel:   "MiniMax-M2.7",
	},
	{
		Name:           "moonshotai",
		EnvVars:        []string{"MOONSHOT_API_KEY"},
		DefaultBaseURL: "https://api.moonshot.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"kimi-", "moonshot-"},
		DefaultModel:   "kimi-k2-thinking",
	},
	{
		// OpenCode Zen (pay-as-you-go). Renamed from "opencode" to
		// distinguish it from the "opencode-go" subscription endpoint; the
		// old name still resolves via providerAliases below.
		Name: "opencode-zen",
		// Dedicated key variable so the Zen endpoint never shares credentials
		// with the opencode-go subscription endpoint (OPENCODE_API_KEY).
		EnvVars:        []string{"OPENCODE_ZEN_API_KEY"},
		DefaultBaseURL: "https://opencode.ai/zen/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ForceProxy:     true,
		Gateway:        true,
		// models.dev keys the Zen gateway as "opencode"; without this the
		// provider missed the catalog entirely (windows fell back to 1M and
		// reasoning levels to the family ladder).
		ModelsDevID: "opencode",
	},
	{
		Name:           "opencode-go",
		EnvVars:        []string{"OPENCODE_API_KEY"},
		DefaultBaseURL: "https://opencode.ai/zen/go/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ForceProxy:     true,
		Gateway:        true,
		// The gateway also serves the Messages wire at {base}/messages, which is
		// how its Claude models are reached (probed live with claude-haiku-5-5:
		// x-api-key auth, plus the x-opencode-session routing header every
		// opencode endpoint requires — attached automatically because the driver
		// carries this provider's name). The OpenAI-family endpoints serve the
		// rest of the lineup and stay the default.
		WireVariants: []WireVariant{
			{Protocol: ProtocolAnthropic, AuthScheme: AuthXAPIKey},
			// Gemini models are served on the native v1beta wire (its models
			// page lists …/google/v1beta/models/<id> for every Gemini id). The
			// selector is declared so selection and the banner tell the truth;
			// the resolver reports the missing driver when one is chosen.
			{Protocol: ProtocolGemini, AuthScheme: AuthBearer},
		},
		// Muse spark advertises "max" in its error text but rejects it on the
		// wire; xhigh is the highest usable rung on this gateway.
		ReasoningLadders: []ReasoningLadder{{
			Match: "muse",
			Ladder: []agentcore.ThinkingLevel{
				agentcore.ThinkingMinimal, agentcore.ThinkingLow, agentcore.ThinkingMedium,
				agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
			},
		}},
	},
	{
		Name:           "kimi-coding",
		EnvVars:        []string{"KIMI_API_KEY"},
		DefaultBaseURL: "https://api.kimi.com/coding",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi",
		EnvVars:        []string{"XIAOMI_API_KEY"},
		DefaultBaseURL: "https://api.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"mimo-"},
		DefaultModel:   "mimo-v2-pro",
	},
	// Alibaba Cloud DashScope, OpenAI-compatible mode. The other Chinese cloud
	// platforms (Qianfan, Volcengine Ark, Hunyuan) were removed as thin aliases
	// onto OpenAI-compatible endpoints with no behaviour of their own; this one
	// stays for its qwen- family, which is what makes the entry worth carrying.
	{
		Name:           "dashscope",
		EnvVars:        []string{"DASHSCOPE_API_KEY"},
		DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"qwen-"},
		DefaultModel:   "qwen-max",
	},
	// The enterprise chain, of which one entry is kept: Azure OpenAI carries the
	// most machinery of the five (endpoint composed from a resource name or a
	// base URL, an API version, and a deployment map) and is the endpoint most
	// enterprises actually deploy. The other four resolvers — Bedrock, Vertex and
	// both Cloudflare forms — remain in special_auth.go, tested, for the day one
	// of them is wanted back: an entry here is all it takes.
	{
		Name:    "azure-openai-responses",
		EnvVars: []string{"AZURE_OPENAI_API_KEY"},
		// Endpoint is composed from AZURE_OPENAI_BASE_URL / AZURE_OPENAI_RESOURCE_NAME
		// per the Azure OpenAI convention; there is no fixed public default.
		DefaultBaseURL: "",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthAzure,
		BaseURLEnvVars: []string{"AZURE_OPENAI_BASE_URL"},
	},
}

// providerRegistryByName indexes providerRegistry by provider name for O(1)
// lookup. Built once at package init.
var providerRegistryByName = func() map[string]ProviderSpec {
	m := make(map[string]ProviderSpec, len(providerRegistry))
	for _, spec := range providerRegistry {
		m[spec.Name] = spec
	}
	return m
}()

// providerAliases maps retired provider names to their canonical registry
// name, so existing configs, flags, and stored sessions keep working after a
// rename. Aliases resolve inside LookupProviderSpec, so every consumer (auth,
// resolution, catalog keys, help text) sees the canonical name; the alias
// itself is never listed by ProviderSpecs or ProviderNames.
var providerAliases = map[string]string{
	"opencode": "opencode-zen",
}

// LookupProviderSpec returns the ProviderSpec for a provider name and whether it
// is a known built-in provider. The returned spec is a copy; mutating its slice
// or map fields is discouraged as they are shared with the registry.
func LookupProviderSpec(name string) (ProviderSpec, bool) {
	if target, ok := providerAliases[name]; ok {
		name = target
	}
	spec, ok := providerRegistryByName[name]
	return spec, ok
}

// APIKeyEnvHint returns a human-readable hint naming the environment variable(s)
// that satisfy the provider's credential check, for embedding in missing-key
// errors (issue #564: "openrouter: missing API key" alone leaves the user
// guessing what to set). Unknown providers fall back to the generic
// <PROVIDER>_API_KEY convention.
func APIKeyEnvHint(name string) string {
	if spec, ok := LookupProviderSpec(name); ok && len(spec.EnvVars) > 0 {
		return strings.Join(spec.EnvVars, " or ")
	}
	return strings.ToUpper(name) + "_API_KEY"
}

// ProviderSpecs returns all built-in provider specs in registry (display) order.
// Callers must not mutate the returned specs' slice/map fields.
func ProviderSpecs() []ProviderSpec {
	out := make([]ProviderSpec, len(providerRegistry))
	copy(out, providerRegistry)
	return out
}

// ProviderNames returns all built-in provider names in registry order.
func ProviderNames() []string {
	out := make([]string, len(providerRegistry))
	for i, spec := range providerRegistry {
		out[i] = spec.Name
	}
	return out
}
