package provider

import (
	"sort"
	"testing"
)

// TestOpencodeGoDeclaresMessagesVariant pins the declaration the wire-variant
// path rests on: opencode-go stays OpenAI-wired by default (nothing changes for
// a user who does not ask), and declares its Messages endpoint as an extra wire
// with the auth scheme that endpoint expects — probed live, x-api-key.
func TestOpencodeGoDeclaresMessagesVariant(t *testing.T) {
	spec, ok := LookupProviderSpec("opencode-go")
	if !ok {
		t.Fatal("opencode-go spec missing")
	}
	if spec.Protocol != ProtocolOpenAI {
		t.Errorf("Protocol = %q, want the OpenAI family to stay the default", spec.Protocol)
	}
	if len(spec.WireVariants) != 1 {
		t.Fatalf("WireVariants = %+v, want one declared wire", spec.WireVariants)
	}
	if v := spec.WireVariants[0]; v.Protocol != ProtocolAnthropic || v.AuthScheme != AuthXAPIKey {
		t.Errorf("variant = %+v, want {anthropic x-api-key}", v)
	}
}

func TestLookupProviderSpec_Hit(t *testing.T) {
	spec, ok := LookupProviderSpec("deepseek")
	if !ok {
		t.Fatalf("LookupProviderSpec(deepseek): expected hit, got miss")
	}
	if spec.Name != "deepseek" {
		t.Errorf("Name = %q, want deepseek", spec.Name)
	}
	if spec.DefaultBaseURL != "https://api.deepseek.com" {
		t.Errorf("DefaultBaseURL = %q, want https://api.deepseek.com", spec.DefaultBaseURL)
	}
	if spec.Protocol != ProtocolOpenAIResponses {
		t.Errorf("Protocol = %q, want %q", spec.Protocol, ProtocolOpenAIResponses)
	}
	if len(spec.EnvVars) != 1 || spec.EnvVars[0] != "DEEPSEEK_API_KEY" {
		t.Errorf("EnvVars = %v, want [DEEPSEEK_API_KEY]", spec.EnvVars)
	}
}

func TestLookupProviderSpec_Miss(t *testing.T) {
	if _, ok := LookupProviderSpec("does-not-exist"); ok {
		t.Errorf("LookupProviderSpec(does-not-exist): expected miss, got hit")
	}
}

func TestProviderAliasOpencodeZen(t *testing.T) {
	spec, ok := LookupProviderSpec("opencode")
	if !ok {
		t.Fatalf("LookupProviderSpec(opencode): expected alias hit, got miss")
	}
	if spec.Name != "opencode-zen" {
		t.Errorf("Name = %q, want opencode-zen", spec.Name)
	}
	if !IsProviderName("opencode") {
		t.Errorf("IsProviderName(opencode): expected true via alias")
	}
}

func TestAnthropicEnvVarOrder(t *testing.T) {
	spec, ok := LookupProviderSpec("anthropic")
	if !ok {
		t.Fatal("LookupProviderSpec(anthropic): expected hit")
	}
	want := []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY"}
	if len(spec.EnvVars) != len(want) {
		t.Fatalf("EnvVars = %v, want %v", spec.EnvVars, want)
	}
	for i := range want {
		if spec.EnvVars[i] != want[i] {
			t.Errorf("EnvVars[%d] = %q, want %q (OAuth must be first)", i, spec.EnvVars[i], want[i])
		}
	}
	if spec.Protocol != ProtocolAnthropic {
		t.Errorf("Protocol = %q, want %q", spec.Protocol, ProtocolAnthropic)
	}
}

// TestChineseCloudProvider pins the one Chinese cloud endpoint the registry
// keeps. Its three siblings (Qianfan, Volcengine Ark, Hunyuan) were retired as
// thin aliases; this entry stays for the qwen- family, so the test asserts the
// metadata that makes it worth carrying rather than a count of platforms.
func TestChineseCloudProvider(t *testing.T) {
	cases := []struct {
		name    string
		envVars []string
		baseURL string
	}{
		{"dashscope", []string{"DASHSCOPE_API_KEY"}, "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := LookupProviderSpec(tc.name)
			if !ok {
				t.Fatalf("LookupProviderSpec(%q): expected hit", tc.name)
			}
			if spec.Name != tc.name {
				t.Errorf("Name = %q, want %q", spec.Name, tc.name)
			}
			if spec.DefaultBaseURL != tc.baseURL {
				t.Errorf("DefaultBaseURL = %q, want %q", spec.DefaultBaseURL, tc.baseURL)
			}
			if spec.Protocol != ProtocolOpenAI {
				t.Errorf("Protocol = %q, want %q", spec.Protocol, ProtocolOpenAI)
			}
			if spec.AuthScheme != AuthBearer {
				t.Errorf("AuthScheme = %q, want %q", spec.AuthScheme, AuthBearer)
			}
			if len(spec.EnvVars) != len(tc.envVars) {
				t.Fatalf("EnvVars = %v, want %v", spec.EnvVars, tc.envVars)
			}
			for i := range tc.envVars {
				if spec.EnvVars[i] != tc.envVars[i] {
					t.Errorf("EnvVars[%d] = %q, want %q", i, spec.EnvVars[i], tc.envVars[i])
				}
			}
		})
	}
}

func TestAzureBaseURLEnvVars(t *testing.T) {
	spec, ok := LookupProviderSpec("azure-openai-responses")
	if !ok {
		t.Fatal("LookupProviderSpec(azure-openai-responses): expected hit")
	}
	found := false
	for _, v := range spec.BaseURLEnvVars {
		if v == "AZURE_OPENAI_BASE_URL" {
			found = true
		}
	}
	if !found {
		t.Errorf("BaseURLEnvVars = %v, want to contain AZURE_OPENAI_BASE_URL", spec.BaseURLEnvVars)
	}
}

func TestRegistryContainsAllExpectedProviders(t *testing.T) {
	expected := []string{
		"anthropic", "openai", "deepseek", "google",
		"groq", "cerebras", "xai", "openrouter", "zai",
		"typesafe",
		"mistral", "minimax", "moonshotai",
		"opencode-zen", "opencode-go", "kimi-coding", "xiaomi", "dashscope",
		"azure-openai-responses",
	}
	for _, name := range expected {
		if _, ok := LookupProviderSpec(name); !ok {
			t.Errorf("registry missing expected provider %q", name)
		}
	}
	names := ProviderNames()
	if len(names) != len(expected) {
		t.Errorf("registry has %d providers, want %d", len(names), len(expected))
	}

	// Every spec must have a name, at least one env var, and a valid protocol.
	for _, spec := range ProviderSpecs() {
		if spec.Name == "" {
			t.Error("found spec with empty Name")
		}
		if len(spec.EnvVars) == 0 {
			t.Errorf("provider %q has no EnvVars", spec.Name)
		}
		if spec.Protocol != ProtocolOpenAI && spec.Protocol != ProtocolAnthropic && spec.Protocol != ProtocolOpenAIResponses {
			t.Errorf("provider %q has invalid Protocol %q", spec.Name, spec.Protocol)
		}
	}

	// No duplicate provider names.
	seen := make(map[string]bool, len(names))
	dupSorted := append([]string(nil), names...)
	sort.Strings(dupSorted)
	for _, n := range dupSorted {
		if seen[n] {
			t.Errorf("duplicate provider name %q in registry", n)
		}
		seen[n] = true
	}
}
