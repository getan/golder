package provider

// Tests for provider resolution moved from cmd/pigo (US-004, #361): ResolveProvider
// maps a model id to the right gateway (preset catalog first, then prefix rules,
// then OpenRouter default), and ResolveBaseURL applies the base-url override
// precedence. Environment lookups are injected via os.Getenv here.

import (
	"os"
	"strings"
	"testing"
)

// TestResolveProviderPresetCatalog verifies a preset id resolves to its declared
// provider (NVIDIA and Ollama presets do not fall through to OpenRouter).
func TestResolveProviderPresetCatalog(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
	}{
		{"meta/llama-3.3-70b-instruct", "nvidia"},     // NVIDIA preset
		{"ollama/llama3.3", "ollama"},                 // Ollama preset
		{"openai/gpt-4o", "openrouter"},               // OpenRouter preset
		{"anthropic/claude-3.5-sonnet", "openrouter"}, // OpenRouter preset
	}
	for _, c := range cases {
		_, name, err := ResolveProvider(c.model, "", "", "", os.Getenv)
		if err != nil {
			t.Errorf("ResolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("ResolveProvider(%q) = %q, want %q", c.model, name, c.wantName)
		}
	}
}

// TestResolveProviderPrefixAndDefault verifies the prefix rules and the
// OpenRouter default for ids not in the catalog.
func TestResolveProviderPrefixAndDefault(t *testing.T) {
	cases := []struct {
		model    string
		baseURL  string
		wantName string
	}{
		{"ollama/some-local-model", "", "ollama"}, // ollama/ prefix
		{"nvidia/some-nim-model", "", "nvidia"},   // nvidia/ prefix
		{"some-unknown-model", "", "openrouter"},  // default
		{"m", "http://host:11434/v1", "ollama"},   // ollama port
	}
	for _, c := range cases {
		_, name, err := ResolveProvider(c.model, c.baseURL, "", "", os.Getenv)
		if err != nil {
			t.Errorf("ResolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("ResolveProvider(%q, %q) = %q, want %q", c.model, c.baseURL, name, c.wantName)
		}
	}
}

// TestResolveProviderExplicitProtocol verifies an explicit --protocol wins over
// model-id heuristics: openai (with base-url) and anthropic select the matching
// wire driver, an empty base-url for openai errors, and an unknown protocol
// errors instead of silently falling back.
func TestResolveProviderExplicitProtocol(t *testing.T) {
	// openai protocol → "openai" provider name, requires base-url.
	if _, name, err := ResolveProvider("any-model", "https://example.com/v1", "openai", "", os.Getenv); err != nil || name != "openai" {
		t.Errorf("protocol=openai = (%q, %v), want (openai, nil)", name, err)
	}
	if _, _, err := ResolveProvider("any-model", "", "openai", "", os.Getenv); err == nil {
		t.Error("protocol=openai with no base-url should error")
	}
	// anthropic protocol → "anthropic" provider name, base-url optional (defaults).
	if _, name, err := ResolveProvider("claude-x", "", "anthropic", "", os.Getenv); err != nil || name != "anthropic" {
		t.Errorf("protocol=anthropic = (%q, %v), want (anthropic, nil)", name, err)
	}
	// Unknown protocol errors rather than falling back to a heuristic.
	if _, _, err := ResolveProvider("any-model", "", "grpc", "", os.Getenv); err == nil {
		t.Error("unknown protocol should error")
	}
}

// TestResolveProviderResponsesProtocol verifies the openai/resp_api selector
// routes to the Responses driver (against an explicit base-url), that the
// "openai/chat" alias resolves identically to "openai", and that resp_api with
// no base-url errors like the plain openai path (mirroring the base-url
// requirement rather than defaulting to a public endpoint).
func TestResolveProviderResponsesProtocol(t *testing.T) {
	// openai/resp_api → "openai" provider name, backed by the Responses driver.
	p, name, err := ResolveProvider("any-model", "https://example.com/v1", "openai/resp_api", "", os.Getenv)
	if err != nil || name != "openai" {
		t.Fatalf("protocol=openai/resp_api = (%q, %v), want (openai, nil)", name, err)
	}
	if _, ok := p.(*responsesDriver); !ok {
		t.Errorf("protocol=openai/resp_api built %T, want *responsesDriver", p)
	}
	// resp_api with no base-url errors, mirroring the openai requirement.
	if _, _, err := ResolveProvider("any-model", "", "openai/resp_api", "", os.Getenv); err == nil {
		t.Error("protocol=openai/resp_api with no base-url should error")
	}
	// "openai/chat" is an alias of "openai": same driver, same base-url rule.
	p, name, err = ResolveProvider("any-model", "https://example.com/v1", "openai/chat", "", os.Getenv)
	if err != nil || name != "openai" {
		t.Fatalf("protocol=openai/chat = (%q, %v), want (openai, nil)", name, err)
	}
	if _, ok := p.(*responsesDriver); ok {
		t.Error("protocol=openai/chat should build the Chat Completions driver, not *responsesDriver")
	}
	if _, _, err := ResolveProvider("any-model", "", "openai/chat", "", os.Getenv); err == nil {
		t.Error("protocol=openai/chat with no base-url should error")
	}
}

// TestResolveProviderExplicitProvider verifies that --provider selects a
// built-in provider from the registry: the returned provider-name is the spec
// name (so key resolution reads the right env var), an OpenAI-protocol provider
// (deepseek) and an Anthropic-protocol provider (minimax) both resolve, an
// incompatible --protocol is a conflict error naming both flags, and an unknown
// provider name errors while listing the available names.
func TestResolveProviderExplicitProvider(t *testing.T) {
	// OpenAI-protocol provider: returns its own name for key lookup.
	if _, name, err := ResolveProvider("deepseek-chat", "", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Anthropic-protocol provider.
	if _, name, err := ResolveProvider("MiniMax-M2", "", "", "minimax", os.Getenv); err != nil || name != "minimax" {
		t.Errorf("provider=minimax = (%q, %v), want (minimax, nil)", name, err)
	}
	// A matching --protocol is not a conflict (deepseek speaks openai/resp_api).
	if _, name, err := ResolveProvider("deepseek-chat", "", "openai/resp_api", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek + protocol=openai/resp_api = (%q, %v), want (deepseek, nil)", name, err)
	}
	// --provider wins over model-id heuristics: an ollama/-prefixed id still
	// resolves to the named provider, not local Ollama.
	if _, name, err := ResolveProvider("ollama/x", "", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek with ollama/ model = (%q, %v), want (deepseek, nil)", name, err)
	}
	// --base-url overrides the spec default without changing the provider name.
	if _, name, err := ResolveProvider("deepseek-chat", "https://proxy.local/v1", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek + base-url = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Conflict: minimax speaks anthropic; forcing --protocol openai errors and
	// names both flags.
	_, _, err := ResolveProvider("MiniMax-M2", "", "openai", "minimax", os.Getenv)
	if err == nil {
		t.Fatal("provider=minimax + protocol=openai should conflict")
	}
	if !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "--protocol") {
		t.Errorf("conflict error should name both flags, got: %v", err)
	}
	// Unknown provider errors and lists available names.
	_, _, err = ResolveProvider("m", "", "", "no-such-provider", os.Getenv)
	if err == nil {
		t.Fatal("unknown provider should error")
	}
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("unknown-provider error should list available names, got: %v", err)
	}
	// An invalid --protocol paired with a named provider surfaces the clear
	// "unknown --protocol" error (listing the accepted set) rather than a
	// misleading conflict message.
	_, _, err = ResolveProvider("deepseek-chat", "", "openai_api", "deepseek", os.Getenv)
	if err == nil {
		t.Fatal("provider=deepseek + protocol=openai_api should error")
	}
	if !strings.Contains(err.Error(), "unknown --protocol") {
		t.Errorf("invalid --protocol should surface the unknown-protocol error, got: %v", err)
	}
}

// TestResolveProviderCNPresets verifies the Chinese-cloud preset ids route to
// their own provider (not the OpenRouter default) via the LookupPreset branch.
func TestResolveProviderCNPresets(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
	}{
		{"ernie-4.5-turbo-32k", "qianfan"},
		{"doubao-seed-1-6", "volcengine"},
		{"qwen-max", "dashscope"},
		{"hunyuan-turbos-latest", "hunyuan"},
	}
	for _, c := range cases {
		_, name, err := ResolveProvider(c.model, "", "", "", os.Getenv)
		if err != nil {
			t.Errorf("ResolveProvider(%q) error: %v", c.model, err)
			continue
		}
		if name != c.wantName {
			t.Errorf("ResolveProvider(%q) = %q, want %q", c.model, name, c.wantName)
		}
	}
}

// TestResolveProviderCNExplicit verifies --provider selects the CN providers
// directly and that --base-url overrides without changing the provider name.
func TestResolveProviderCNExplicit(t *testing.T) {
	for _, name := range []string{"qianfan", "volcengine", "dashscope", "hunyuan"} {
		if _, got, err := ResolveProvider("some-model", "", "", name, os.Getenv); err != nil || got != name {
			t.Errorf("provider=%s = (%q, %v), want (%s, nil)", name, got, err, name)
		}
		if _, got, err := ResolveProvider("some-model", "https://proxy.local/v1", "", name, os.Getenv); err != nil || got != name {
			t.Errorf("provider=%s + base-url = (%q, %v), want (%s, nil)", name, got, err, name)
		}
	}
}

// TestResolveProviderModelNameInference verifies model-name inference (Issue
// #235): with only --model given, a bare model name whose prefix identifies a
// single provider resolves to that provider — NOT the OpenRouter default.
func TestResolveProviderModelNameInference(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
	}{
		{"claude-opus-4-8", "anthropic"},
		{"deepseek-chat", "deepseek"},
		{"gpt-4.1", "openai"},
		{"gemini-3-pro", "google"},
		{"grok-5", "xai"},
	}
	for _, c := range cases {
		if _, name, err := ResolveProvider(c.model, "", "", "", os.Getenv); err != nil || name != c.wantName {
			t.Errorf("ResolveProvider(%q) = (%q, %v), want (%q, nil)", c.model, name, err, c.wantName)
		}
	}
}

// TestResolveProviderInferencePrecedence verifies that model-name inference does
// not override explicit flags and does not fire when a --base-url is given, and
// that unknown/ambiguous names still fall back to OpenRouter.
func TestResolveProviderInferencePrecedence(t *testing.T) {
	// Explicit --provider wins over an inferable model name.
	if _, name, err := ResolveProvider("claude-opus-4-8", "", "", "deepseek", os.Getenv); err != nil || name != "deepseek" {
		t.Errorf("provider=deepseek overrides inference = (%q, %v), want (deepseek, nil)", name, err)
	}
	// Explicit --protocol wins over an inferable model name.
	if _, name, err := ResolveProvider("claude-opus-4-8", "https://example.com/v1", "openai", "", os.Getenv); err != nil || name != "openai" {
		t.Errorf("protocol=openai overrides inference = (%q, %v), want (openai, nil)", name, err)
	}
	// A --base-url signals a custom endpoint: inference is skipped, default applies.
	if _, name, err := ResolveProvider("claude-opus-4-8", "https://gw.local/v1", "", "", os.Getenv); err != nil || name != "openrouter" {
		t.Errorf("inference skipped with base-url = (%q, %v), want (openrouter, nil)", name, err)
	}
	// Ambiguous/unknown names still default to OpenRouter.
	for _, m := range []string{"llama-3.3-70b", "totally-unknown-model"} {
		if _, name, err := ResolveProvider(m, "", "", "", os.Getenv); err != nil || name != "openrouter" {
			t.Errorf("ResolveProvider(%q) = (%q, %v), want (openrouter, nil)", m, name, err)
		}
	}
}

// TestResolveBaseURLPrecedence exercises all four precedence levels for a
// hyphenated provider (zai-coding-cn → ZAI_CODING_CN_BASE_URL).
func TestResolveBaseURLPrecedence(t *testing.T) {
	spec, ok := LookupProviderSpec("zai-coding-cn")
	if !ok {
		t.Fatal("expected zai-coding-cn in registry")
	}
	if got := ResolveBaseURL(spec, "", os.Getenv); got != spec.DefaultBaseURL {
		t.Errorf("default: got %q, want %q", got, spec.DefaultBaseURL)
	}
	t.Setenv("ZAI_CODING_CN_BASE_URL", "https://generic.example/v4")
	if got := ResolveBaseURL(spec, "", os.Getenv); got != "https://generic.example/v4" {
		t.Errorf("generic env: got %q, want %q", got, "https://generic.example/v4")
	}
	if got := ResolveBaseURL(spec, "https://flag.example/v4", os.Getenv); got != "https://flag.example/v4" {
		t.Errorf("flag over generic: got %q, want %q", got, "https://flag.example/v4")
	}
}

// TestResolveBaseURLProviderSpecificEnv covers a provider that declares a
// provider-specific base-url env var (azure), asserting it sits between the flag
// and the generic convention in precedence.
func TestResolveBaseURLProviderSpecificEnv(t *testing.T) {
	spec, ok := LookupProviderSpec("azure-openai-responses")
	if !ok {
		t.Fatal("expected azure-openai-responses in registry")
	}
	if len(spec.BaseURLEnvVars) == 0 {
		t.Fatal("expected azure-openai-responses to declare BaseURLEnvVars")
	}
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://specific.example")
	if got := ResolveBaseURL(spec, "", os.Getenv); got != "https://specific.example" {
		t.Errorf("provider-specific env: got %q, want %q", got, "https://specific.example")
	}
	t.Setenv("AZURE_OPENAI_RESPONSES_BASE_URL", "https://generic.example")
	if got := ResolveBaseURL(spec, "", os.Getenv); got != "https://specific.example" {
		t.Errorf("provider-specific beats generic: got %q, want %q", got, "https://specific.example")
	}
	if got := ResolveBaseURL(spec, "https://flag.example", os.Getenv); got != "https://flag.example" {
		t.Errorf("flag beats provider-specific: got %q, want %q", got, "https://flag.example")
	}
}

// TestCanonicalizeModelBareProviderName verifies a bare built-in provider name
// maps to that provider's first preset id (issue #564), while real preset ids,
// routed "provider/model" ids, and unknown names pass through unchanged.
func TestCanonicalizeModelBareProviderName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"zai", "glm-4.7"},
		{"ZAI", "glm-4.7"},
		{"  deepseek  ", "deepseek-v4-flash"},
		{"glm-4.7", "glm-4.7"},
		{"openai/gpt-4o", "openai/gpt-4o"},
		{"not-a-provider", "not-a-provider"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := CanonicalizeModel(tc.in); got != tc.want {
			t.Errorf("CanonicalizeModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveProviderBareProviderNameUsesDefaultModel verifies /model zai style
// shorthand resolves to the zai provider with a real wire model id, instead of
// silently falling back to OpenRouter with the literal id "zai".
func TestResolveProviderBareProviderNameUsesDefaultModel(t *testing.T) {
	prov, name, err := ResolveProvider("zai", "", "", "", os.Getenv)
	if err != nil {
		t.Fatalf("ResolveProvider(zai): %v", err)
	}
	if name != "zai" {
		t.Errorf("provider name = %q, want %q", name, "zai")
	}
	models := prov.Models()
	if len(models) != 1 || models[0].ID != "glm-4.7" || models[0].Provider != "zai" {
		t.Errorf("models = %+v, want one zai/glm-4.7 entry", models)
	}
}

// TestResolveProviderBareProviderWithoutPresetsErrors verifies a bare provider
// name whose provider has no preset models surfaces a clear mismatch error
// rather than the old silent OpenRouter fallback (issue #564).
func TestResolveProviderBareProviderWithoutPresetsErrors(t *testing.T) {
	var name string
	for _, spec := range ProviderSpecs() {
		if len(PresetsByProvider(spec.Name)) == 0 {
			name = spec.Name
			break
		}
	}
	if name == "" {
		t.Skip("every built-in provider has at least one preset model")
	}
	_, _, err := ResolveProvider(name, "", "", "", os.Getenv)
	if err == nil {
		t.Fatalf("ResolveProvider(%q) succeeded, want provider-name mismatch error", name)
	}
	if !strings.Contains(err.Error(), "names a provider") {
		t.Errorf("error = %q, want it to explain the provider/model mismatch", err.Error())
	}
}

// TestResolveProviderAnthropicPresets verifies the Fable presets resolve to the
// first-party anthropic provider with the wire id passed through unchanged, and
// that the bare provider name "anthropic" defaults to the newest Fable (the
// anthropic section is ordered newest-first to feed CanonicalizeModel).
func TestResolveProviderAnthropicPresets(t *testing.T) {
	for _, id := range []string{"claude-fable-5", "claude-fable-5-1"} {
		prov, name, err := ResolveProvider(id, "", "", "", os.Getenv)
		if err != nil {
			t.Fatalf("ResolveProvider(%q): %v", id, err)
		}
		if name != "anthropic" {
			t.Errorf("ResolveProvider(%q) provider = %q, want anthropic", id, name)
		}
		models := prov.Models()
		if len(models) != 1 || models[0].ID != id || models[0].Provider != "anthropic" {
			t.Errorf("ResolveProvider(%q) models = %+v, want one anthropic entry with the same id", id, models)
		}
	}
	if got := CanonicalizeModel("anthropic"); got != "claude-fable-5-1" {
		t.Errorf("CanonicalizeModel(anthropic) = %q, want claude-fable-5-1 (newest-first)", got)
	}
}

// TestResolveProviderOpenAIAndNewPresets verifies the OpenAI section resolves to
// the first-party openai provider (bare "openai" defaults to the newest
// flagship, newest-first order), and the new xiaomi/xai ids resolve unchanged.
func TestResolveProviderOpenAIAndNewPresets(t *testing.T) {
	for _, id := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.5"} {
		prov, name, err := ResolveProvider(id, "", "", "", os.Getenv)
		if err != nil {
			t.Fatalf("ResolveProvider(%q): %v", id, err)
		}
		if name != "openai" {
			t.Errorf("ResolveProvider(%q) provider = %q, want openai", id, name)
		}
		models := prov.Models()
		if len(models) != 1 || models[0].ID != id || models[0].Provider != "openai" {
			t.Errorf("ResolveProvider(%q) models = %+v, want one openai entry with the same id", id, models)
		}
	}
	if got := CanonicalizeModel("openai"); got != "gpt-6-astra" {
		t.Errorf("CanonicalizeModel(openai) = %q, want gpt-6-astra (newest-first)", got)
	}
	provGoogle, googleName, gerr := ResolveProvider("google", "", "", "", os.Getenv)
	if gerr != nil {
		t.Fatalf("ResolveProvider(google): %v", gerr)
	}
	if googleName != "google" {
		t.Errorf("ResolveProvider(google) provider = %q, want google", googleName)
	}
	if models := provGoogle.Models(); len(models) != 1 || models[0].ID != "gemini-3.8-flash" {
		t.Errorf("ResolveProvider(google) models = %+v, want one gemini-3.8-flash entry", models)
	}
	for _, tc := range []struct{ id, provider string }{
		{"gemini-3.8-flash", "google"},
		{"mimo-v2-flash", "xiaomi"},
		{"grok-4.6", "xai"},
		{"glm-5.3-flash", "zai"},
		{"deepseek-flash", "deepseek"},
	} {
		_, name, err := ResolveProvider(tc.id, "", "", "", os.Getenv)
		if err != nil {
			t.Fatalf("ResolveProvider(%q): %v", tc.id, err)
		}
		if name != tc.provider {
			t.Errorf("ResolveProvider(%q) provider = %q, want %q", tc.id, name, tc.provider)
		}
	}
}

func TestResolveNamedProviderFamilyHeuristic(t *testing.T) {
	// Family model on an OpenAI-wired provider upgrades to the Responses driver.
	p, _, err := ResolveNamedProvider("opencode-go", "muse-spark-1.3", "", "", os.Getenv)
	if err != nil {
		t.Fatalf("family model error: %v", err)
	}
	if _, ok := p.(*responsesDriver); !ok {
		t.Errorf("muse-spark on opencode-go = %T, want *responsesDriver", p)
	}
	// Non-family model stays on Chat Completions, carrying the spec name so
	// per-provider request decoration (session header) applies.
	p, _, err = ResolveNamedProvider("opencode-go", "qwen-max", "", "", os.Getenv)
	if err != nil {
		t.Fatalf("non-family model error: %v", err)
	}
	if _, ok := p.(*openAICompatDriver); !ok {
		t.Errorf("qwen-max on opencode-go = %T, want *openAICompatDriver", p)
	}
	if p.Name() != "opencode-go" {
		t.Errorf("driver name = %q, want opencode-go", p.Name())
	}
	// Explicit resp_api on an OpenAI-wired provider is an upgrade, not a conflict.
	p, _, err = ResolveNamedProvider("opencode-go", "qwen-max", "", "openai/resp_api", os.Getenv)
	if err != nil {
		t.Fatalf("explicit resp_api error: %v", err)
	}
	if _, ok := p.(*responsesDriver); !ok {
		t.Errorf("explicit resp_api = %T, want *responsesDriver", p)
	}
	// Explicit chat wins over the family heuristic.
	p, _, err = ResolveNamedProvider("opencode-go", "gpt-4o", "", "openai", os.Getenv)
	if err != nil {
		t.Fatalf("explicit openai error: %v", err)
	}
	if _, ok := p.(*openAICompatDriver); !ok {
		t.Errorf("explicit openai = %T, want *openAICompatDriver", p)
	}
	// Incompatible explicit protocol still errors.
	if _, _, err = ResolveNamedProvider("opencode-go", "gpt-4o", "", "anthropic", os.Getenv); err == nil {
		t.Error("anthropic protocol on opencode-go should error")
	}
	// Anthropic-wired providers never take the heuristic path.
	if _, name, err := ResolveNamedProvider("anthropic", "claude-opus-4-8", "", "", os.Getenv); err != nil || name != "anthropic" {
		t.Errorf("anthropic provider = (%q, %v), want (anthropic, nil)", name, err)
	}
}
