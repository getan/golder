package provider

// Tests for model-name → provider inference (InferProviderFromModel, defined
// in resolve.go next to its only caller). They verify each documented name
// prefix resolves to the expected built-in provider, that ambiguous/unknown
// ids and routed "provider/model" ids do not resolve, that matching is
// case-insensitive, and that every declared prefix belongs to exactly one
// provider (the registry is the single source of truth).

import (
	"strings"
	"testing"
)

// TestInferProviderFromModelKnown verifies each documented model-name prefix
// resolves to its expected built-in provider.
func TestInferProviderFromModelKnown(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-8", "anthropic"},
		{"claude-3.5-sonnet", "anthropic"},
		{"fable-5", "anthropic"},
		{"fable-5.1", "anthropic"},
		{"gpt-4o", "openai"},
		{"gpt-4o-mini", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"o4-mini", "openai"},
		{"gemini-2.5-pro", "google"},
		{"deepseek-chat", "deepseek"},
		{"deepseek-v4-pro", "deepseek"},
		{"glm-5.1", "zai"},
		{"kimi-k2-thinking", "moonshotai"},
		{"moonshot-v1-8k", "moonshotai"},
		{"qwen-max", "dashscope"},
		{"ernie-4.5-turbo-32k", "qianfan"},
		{"doubao-seed-1-6", "volcengine"},
		{"grok-4.5", "xai"},
		{"mistral-large-latest", "mistral"},
		{"codestral-latest", "mistral"},
		{"devstral-medium-latest", "mistral"},
		{"hunyuan-turbos-latest", "hunyuan"},
		{"minimax-m2.7", "minimax"},
		{"mimo-v2-pro", "xiaomi"},
	}
	for _, c := range cases {
		got, ok := InferProviderFromModel(c.model)
		if !ok {
			t.Errorf("InferProviderFromModel(%q): ok=false, want provider %q", c.model, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("InferProviderFromModel(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

// TestInferProviderFromModelCaseInsensitive verifies matching ignores case and
// surrounding whitespace.
func TestInferProviderFromModelCaseInsensitive(t *testing.T) {
	for _, m := range []string{"Claude-Opus-4-8", "  GPT-4o  ", "DeepSeek-Chat"} {
		if _, ok := InferProviderFromModel(m); !ok {
			t.Errorf("InferProviderFromModel(%q): ok=false, want a match", m)
		}
	}
}

// TestInferProviderFromModelAmbiguousOrUnknown verifies ids that are ambiguous
// (served by many gateways), routed ("provider/model"), empty, or simply
// unknown do NOT resolve — the caller must fall through to its default.
func TestInferProviderFromModelAmbiguousOrUnknown(t *testing.T) {
	for _, m := range []string{
		"",                       // empty
		"   ",                    // whitespace only
		"llama-3.3-70b-instruct", // ambiguous: many gateways
		"qwq-32b",                // ambiguous
		"gemma-2-9b-it",          // ambiguous
		"mixtral-8x22b",          // ambiguous
		"openai/gpt-4o",          // routed id, left to the default gateway
		"anthropic/claude-3.5",   // routed id
		"ollama/llama3.3",        // routed id (ollama prefix path)
		"totally-made-up-model",  // unknown
	} {
		if got, ok := InferProviderFromModel(m); ok {
			t.Errorf("InferProviderFromModel(%q) = (%q, true), want ok=false", m, got)
		}
	}
}

// TestInferProviderPrefixesAreOwnedAndUnambiguous verifies the registry's
// ModelPrefixes invariants: each declared prefix resolves back to its own
// provider, and no prefix is claimed by two providers (which would make
// inference order-dependent).
func TestInferProviderPrefixesAreOwnedAndUnambiguous(t *testing.T) {
	for _, spec := range providerRegistry {
		for _, prefix := range spec.ModelPrefixes {
			if got, ok := InferProviderFromModel(prefix + "x"); !ok || got != spec.Name {
				t.Errorf("prefix %q of %q infers (%q, %v), want (%q, true)", prefix, spec.Name, got, ok, spec.Name)
			}
			for _, other := range providerRegistry {
				if other.Name == spec.Name {
					continue
				}
				for _, otherPrefix := range other.ModelPrefixes {
					if strings.HasPrefix(otherPrefix, prefix) || strings.HasPrefix(prefix, otherPrefix) {
						t.Errorf("providers %q and %q claim overlapping model prefixes (%q, %q)", spec.Name, other.Name, prefix, otherPrefix)
					}
				}
			}
		}
	}
}
