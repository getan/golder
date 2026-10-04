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
	// ExtraHeaders are provider-specific headers attached to every request (may
	// be nil).
	ExtraHeaders map[string]string
	// BaseURLEnvVars lists provider-specific base-URL override environment
	// variables (e.g. AZURE_OPENAI_BASE_URL), in precedence order. May be empty;
	// the generic <PROVIDER>_BASE_URL convention is handled by callers.
	BaseURLEnvVars []string
	// ForceProxy marks providers that must go through the proxy when one is
	// configured (opencode.ai from mainland networks; Muse models behind it
	// require US egress). With GOLDER_PROXY unset the request goes direct, so
	// users without a proxy work out of the box. The decision keys on the
	// provider name, so --base-url overrides keep the behavior.
	ForceProxy bool

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
		Name:           "ant-ling",
		EnvVars:        []string{"ANT_LING_API_KEY"},
		DefaultBaseURL: "https://api.ant-ling.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
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
		Name:           "nvidia",
		EnvVars:        []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"},
		DefaultBaseURL: nvidiaBaseURL, // https://integrate.api.nvidia.com/v1
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		DefaultModel:   "meta/llama-3.3-70b-instruct",
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
		DefaultModel:   "openai/gpt-4o",
	},
	{
		Name:           "vercel-ai-gateway",
		EnvVars:        []string{"AI_GATEWAY_API_KEY"},
		DefaultBaseURL: "https://ai-gateway.vercel.sh",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelsDevID:    "vercel",
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
		Name:           "zai-coding-cn",
		EnvVars:        []string{"ZAI_CODING_CN_API_KEY"},
		DefaultBaseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		// The China coding plan serves the same GLM fleet as models.dev's
		// zai-coding-plan entry, so it reuses that entry's ladders.
		ModelsDevID: "zai-coding-plan",
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
		Name:           "minimax-cn",
		EnvVars:        []string{"MINIMAX_CN_API_KEY"},
		DefaultBaseURL: "https://api.minimaxi.com/anthropic",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
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
		Name:           "moonshotai-cn",
		EnvVars:        []string{"MOONSHOT_API_KEY"},
		DefaultBaseURL: "https://api.moonshot.cn/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "huggingface",
		EnvVars:        []string{"HF_TOKEN"},
		DefaultBaseURL: "https://router.huggingface.co/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "fireworks",
		EnvVars:        []string{"FIREWORKS_API_KEY"},
		DefaultBaseURL: "https://api.fireworks.ai/inference",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelsDevID:    "fireworks-ai",
		DefaultModel:   "accounts/fireworks/models/deepseek-v4-pro",
	},
	{
		Name:           "together",
		EnvVars:        []string{"TOGETHER_API_KEY"},
		DefaultBaseURL: "https://api.together.ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelsDevID:    "togetherai",
		DefaultModel:   "deepseek-ai/DeepSeek-V4-Pro",
	},
	{
		Name:           "novita",
		EnvVars:        []string{"NOVITA_API_KEY"},
		DefaultBaseURL: "https://api.novita.ai/openai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelsDevID:    "novita-ai",
		DefaultModel:   "deepseek/deepseek-v4-pro",
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
	},
	{
		Name:           "opencode-go",
		EnvVars:        []string{"OPENCODE_API_KEY"},
		DefaultBaseURL: "https://opencode.ai/zen/go/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ForceProxy:     true,
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
	{
		Name:           "xiaomi-token-plan-cn",
		EnvVars:        []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
		DefaultBaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi-token-plan-ams",
		EnvVars:        []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
		DefaultBaseURL: "https://token-plan-ams.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:           "xiaomi-token-plan-sgp",
		EnvVars:        []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
		DefaultBaseURL: "https://token-plan-sgp.xiaomimimo.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	// Chinese cloud LLM platforms. All four expose OpenAI-compatible endpoints
	// authenticated with a plain Bearer API key, so they reuse the standard
	// OpenAI-compatible driver with no bespoke auth. Base URLs are the platforms'
	// OpenAI-compatible endpoints as documented at implementation time.
	{
		// Baidu AI Cloud Qianfan (Baidu Qianfan).
		Name:           "qianfan",
		EnvVars:        []string{"QIANFAN_API_KEY"},
		DefaultBaseURL: "https://qianfan.baidubce.com/v2",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"ernie-"},
		DefaultModel:   "ernie-4.5-turbo-32k",
	},
	{
		// ByteDance Volcengine Ark (Volcengine Ark). ARK_API_KEY is the platform's
		// conventional variable; VOLCENGINE_API_KEY is accepted as a fallback.
		Name:           "volcengine",
		EnvVars:        []string{"ARK_API_KEY", "VOLCENGINE_API_KEY"},
		DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"doubao-"},
		DefaultModel:   "doubao-seed-1-6",
	},
	{
		// Alibaba Cloud DashScope (DashScope), OpenAI-compatible mode.
		Name:           "dashscope",
		EnvVars:        []string{"DASHSCOPE_API_KEY"},
		DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"qwen-"},
		DefaultModel:   "qwen-max",
	},
	{
		// Tencent Hunyuan (Hunyuan), OpenAI-compatible endpoint.
		Name:           "hunyuan",
		EnvVars:        []string{"HUNYUAN_API_KEY"},
		DefaultBaseURL: "https://api.hunyuan.cloud.tencent.com/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
		ModelPrefixes:  []string{"hunyuan-"},
		DefaultModel:   "hunyuan-turbos-latest",
	},
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
	{
		Name:    "amazon-bedrock",
		EnvVars: []string{"AWS_BEARER_TOKEN_BEDROCK"},
		// Region-specific runtime endpoint; {AWS_REGION} defaults to us-east-1.
		DefaultBaseURL: "https://bedrock-runtime.{AWS_REGION}.amazonaws.com",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthAWS,
	},
	{
		Name:    "google-vertex",
		EnvVars: []string{"GOOGLE_CLOUD_API_KEY"},
		// Location-specific endpoint; protocol varies by model (Gemini vs Claude).
		DefaultBaseURL: "https://{location}-aiplatform.googleapis.com",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthSpecial,
	},
	{
		Name:    "cloudflare-workers-ai",
		EnvVars: []string{"CLOUDFLARE_API_KEY"},
		// {id} is CLOUDFLARE_ACCOUNT_ID.
		DefaultBaseURL: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1",
		Protocol:       ProtocolOpenAI,
		AuthScheme:     AuthBearer,
	},
	{
		Name:    "cloudflare-ai-gateway",
		EnvVars: []string{"CLOUDFLARE_API_KEY"},
		// {acct}/{gw} are CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_GATEWAY_ID.
		DefaultBaseURL: "https://gateway.ai.cloudflare.com/v1/{account_id}/{gateway_id}/anthropic",
		Protocol:       ProtocolAnthropic,
		AuthScheme:     AuthXAPIKey,
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
