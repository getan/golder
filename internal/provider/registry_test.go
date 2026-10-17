package provider

import (
	"reflect"
	"sort"
	"testing"
)

// TestOpencodeGoDeclaresMessagesVariant pins the declarations the wire-variant
// path rests on: opencode-go stays OpenAI-wired by default (nothing changes for
// a user who does not ask), and declares its Messages and Gemini endpoints as
// extra wires with the auth schemes those endpoints expect.
func TestOpencodeGoDeclaresMessagesVariant(t *testing.T) {
	spec, ok := LookupProviderSpec("opencode-go")
	if !ok {
		t.Fatal("opencode-go spec missing")
	}
	if spec.Protocol != ProtocolOpenAI {
		t.Errorf("Protocol = %q, want the OpenAI family to stay the default", spec.Protocol)
	}
	if len(spec.WireVariants) != 2 {
		t.Fatalf("WireVariants = %+v, want Messages and Gemini wires", spec.WireVariants)
	}
	byProtocol := map[string]WireVariant{}
	for _, v := range spec.WireVariants {
		byProtocol[v.Protocol] = v
	}
	if v := byProtocol[ProtocolAnthropic]; v.AuthScheme != AuthXAPIKey {
		t.Errorf("Messages variant = %+v, want x-api-key auth", v)
	}
	if v := byProtocol[ProtocolGemini]; v.AuthScheme != AuthBearer || v.BaseURL == "" {
		t.Errorf("Gemini variant = %+v, want bearer auth and its own base URL", v)
	}
}

// TestProviderDefaultsForSwitching pins the two defaults a /provider switch
// depends on: openai resolves to GPT-6.1 Sol, the family's current balanced
// model (not the Pro-tier Astra), and opencode-go — the multi-vendor gateway
// golder launches on — declares the same model pair the CLI launches with.
// Without the latter,
// switching to opencode-go kept the previous gateway's model in the live
// configuration, so the status bar changed provider but not model.
func TestProviderDefaultsForSwitching(t *testing.T) {
	openai, ok := LookupProviderSpec("openai")
	if !ok || openai.DefaultModel != "gpt-6.1-sol" {
		t.Errorf("openai default = %q, want gpt-6.1-sol", openai.DefaultModel)
	}
	goSpec, ok := LookupProviderSpec("opencode-go")
	if !ok || goSpec.DefaultModel != "deepseek-v4.1-flash" {
		t.Errorf("opencode-go default = %q, want deepseek-v4.1-flash (the CLI's launch pairing)", goSpec.DefaultModel)
	}
	if got := CanonicalizeModel("opencode-go"); got != "deepseek-v4.1-flash" {
		t.Errorf("CanonicalizeModel(opencode-go) = %q, want the declared default", got)
	}
}

// TestEveryProviderHasADefaultModel pins the /provider contract for the whole
// registry: a switch must always land on a model, so every built-in spec
// declares one. The decision model is included because a bare name still has to
// resolve to a real wire id, even though /provider refuses to switch to it.
func TestEveryProviderHasADefaultModel(t *testing.T) {
	for _, spec := range ProviderSpecs() {
		if spec.DefaultModel == "" {
			t.Errorf("provider %q has no DefaultModel; /provider would keep the previous gateway's model", spec.Name)
		}
	}
}

// TestCommandCodeProvider pins the official Provider API wiring: OpenAI-family
// default (Chat + the Responses upgrade), a Messages variant authenticated with
// x-api-key at the same /v1 base, and model-family wire selection for the
// vendor-prefixed ids Command Code publishes.
func TestCommandCodeProvider(t *testing.T) {
	spec, ok := LookupProviderSpec("commandcode")
	if !ok {
		t.Fatal("commandcode spec missing")
	}
	if spec.Protocol != ProtocolOpenAI || spec.AuthScheme != AuthBearer {
		t.Errorf("protocol/auth = %q/%q, want openai/bearer", spec.Protocol, spec.AuthScheme)
	}
	if spec.DefaultBaseURL != "https://api.commandcode.ai/provider/v1" {
		t.Errorf("base URL = %q, want the Provider API /v1 endpoint", spec.DefaultBaseURL)
	}
	if len(spec.EnvVars) != 1 || spec.EnvVars[0] != "COMMANDCODE_API_KEY" {
		t.Errorf("EnvVars = %v, want [COMMANDCODE_API_KEY]", spec.EnvVars)
	}
	if spec.DefaultModel != "deepseek/deepseek-v4-flash" {
		t.Errorf("DefaultModel = %q, want deepseek/deepseek-v4-flash", spec.DefaultModel)
	}
	if len(spec.WireVariants) != 1 {
		t.Fatalf("WireVariants = %+v, want one Messages variant", spec.WireVariants)
	}
	if v := spec.WireVariants[0]; v.Protocol != ProtocolAnthropic || v.AuthScheme != AuthXAPIKey || v.BaseURL != "" {
		t.Errorf("Messages variant = %+v, want anthropic/x-api-key on the shared base URL", v)
	}
	if alias, ok := LookupProviderSpec("command-code"); !ok || alias.Name != "commandcode" {
		t.Errorf("command-code alias = %q,%v, want commandcode,true", alias.Name, ok)
	}
	if got, want := ProviderWires(spec), []Wire{WireResponses, WireMessages, WireChat}; !reflect.DeepEqual(got, want) {
		t.Errorf("ProviderWires = %v, want %v", got, want)
	}

	t.Setenv("GOLDER_HOME", t.TempDir())
	env := func(key string) string {
		switch key {
		case "GOLDER_WIRE_PROBE":
			return "off"
		case "COMMANDCODE_API_KEY":
			return "test-key"
		default:
			return ""
		}
	}
	cases := []struct {
		model  string
		wire   Wire
		driver any
	}{
		{"xiaomi/mimo-v2.6-flash", WireResponses, (*responsesDriver)(nil)},
		{"claude-haiku-5-5", WireMessages, (*anthropicCompatDriver)(nil)},
		{"google/gemini-3.8-flash", WireChat, (*openAICompatDriver)(nil)},
	}
	for _, c := range cases {
		if got := SelectWire(spec, c.model); got != c.wire {
			t.Errorf("SelectWire(%s) = %s, want %s", c.model, got, c.wire)
		}
		p, name, err := ResolveNamedProvider("commandcode", c.model, "", "", env)
		if err != nil {
			t.Errorf("ResolveNamedProvider(%s): %v", c.model, err)
			continue
		}
		if name != "commandcode" {
			t.Errorf("ResolveNamedProvider(%s) name = %q, want commandcode", c.model, name)
		}
		if got := reflect.TypeOf(UnwrapProvider(p)); got != reflect.TypeOf(c.driver) {
			t.Errorf("ResolveNamedProvider(%s) driver = %v, want %v", c.model, got, reflect.TypeOf(c.driver))
		}
	}
}

// TestCommandCodeUsesConfiguredLocalProxy pins the routing requirement for the
// foreign upstreams: when GOLDER_PROXY is set, Command Code joins the
// ForceProxy gateways and uses that local proxy.
func TestCommandCodeUsesConfiguredLocalProxy(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "http://127.0.0.1:7897")
	if got := ProxyStatus("commandcode", "https://api.commandcode.ai/provider/v1"); got != "proxy" {
		t.Errorf("Command Code proxy status = %q, want proxy via the configured local proxy", got)
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
		"opencode-zen", "opencode-go", "commandcode", "kimi-coding", "xiaomi", "dashscope",
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
