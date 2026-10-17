package provider

import "testing"

// TestModelWire pins the provider-independent half of the selection: the wire a
// model's own vendor publishes it on. It is a naming heuristic, and the cases
// are the families the registry actually serves.
func TestModelWire(t *testing.T) {
	cases := []struct {
		id   string
		want Wire
	}{
		{"gpt-5.6-luna", WireResponses},
		{"gpt-6-astra", WireResponses},
		{"o3", WireResponses},
		{"o4-mini", WireResponses},
		{"gpt-5.3-codex", WireResponses},
		{"grok-4.7", WireResponses},
		{"muse-spark-1.3-contributor", WireResponses},
		{"claude-haiku-5-5", WireMessages},
		{"anthropic/claude-opus-5", WireMessages},
		{"gemini-4-argon-contributor", WireGemini},
		{"google/gemini-3.8-flash", WireGemini},
		{"glm-5.3", WireResponses},
		{"deepseek-v4.1-flash", WireResponses},
		{"qwen3.8-max", WireResponses},
		{"kimi-k3", WireResponses},
		{"minimax-m3", WireResponses},
		{"longcat-2.0", WireResponses},
		{"step-5-preview", WireResponses},
		{"hy3", WireResponses},
		{"meta/llama-3.3-70b-instruct", WireChat},
		{"mistral-large-4", WireChat},
		{"space-bunny", WireChat},
		{"", WireChat},
	}
	for _, c := range cases {
		if got := ModelWire(c.id); got != c.want {
			t.Errorf("ModelWire(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestProviderWires pins the capability side: the registered protocol, declared
// variants, and the Responses upgrade an OpenAI-wired provider is assumed to
// serve — in the fixed priority order.
func TestProviderWires(t *testing.T) {
	spec := func(name string) ProviderSpec {
		s, ok := LookupProviderSpec(name)
		if !ok {
			t.Fatalf("provider %q missing from the registry", name)
		}
		return s
	}
	want := map[string][]Wire{
		"opencode-go": {WireResponses, WireMessages, WireGemini, WireChat},
		"openrouter":  {WireResponses, WireChat},
		"anthropic":   {WireMessages},
		"deepseek":    {WireResponses},
		"google":      {WireResponses, WireChat},
	}
	for name, wires := range want {
		got := ProviderWires(spec(name))
		if len(got) != len(wires) {
			t.Errorf("ProviderWires(%s) = %v, want %v", name, got, wires)
			continue
		}
		for i := range wires {
			if got[i] != wires[i] {
				t.Errorf("ProviderWires(%s)[%d] = %q, want %q", name, i, got[i], wires[i])
			}
		}
	}
}

// TestModelFamily pins the grouping the probe cache uses: one confirmed model
// answers for its line, and a routed id is grouped by its model half.
func TestModelFamily(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4.1-flash":     "deepseek",
		"deepseek-v4-pro":         "deepseek",
		"anthropic/claude-opus-5": "claude",
		"google/gemini-3.8-flash": "gemini",
		"glm-5.3":                 "glm",
		"qwen3.8-max":             "qwen",
		"gpt-5.6-luna":            "gpt",
		"o3":                      "o",
		"":                        "",
		"...":                     "",
	}
	for id, want := range cases {
		if got := ModelFamily(id); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestSelectWire pins the static half: the model's native wire when the
// provider serves it, the provider's registered wire otherwise. The domestic
// families are Responses-native now, so a gateway that still routes them over
// Chat is the lazy probe's business (see selectWireWithProbe), not this
// table's.
func TestSelectWire(t *testing.T) {
	spec := func(name string) ProviderSpec {
		s, ok := LookupProviderSpec(name)
		if !ok {
			t.Fatalf("provider %q missing from the registry", name)
		}
		return s
	}
	cases := []struct {
		provider, model string
		want            Wire
	}{
		{"opencode-go", "gpt-5.6-luna", WireResponses},
		{"opencode-go", "grok-4.7", WireResponses},
		{"opencode-go", "muse-spark-1.3-contributor", WireResponses},
		{"opencode-go", "claude-haiku-5-5", WireMessages},
		{"opencode-go", "gemini-4-argon-contributor", WireGemini},
		{"opencode-go", "glm-5.3", WireResponses},
		{"opencode-go", "deepseek-v4.1-flash", WireResponses},
		{"openrouter", "gemini-3.8-flash", WireChat},
		{"openrouter", "gpt-6-luna", WireResponses},
		{"openrouter", "deepseek-v4-pro", WireResponses},
		{"anthropic", "claude-fable-5-1", WireMessages},
		{"deepseek", "deepseek-chat", WireResponses},
		{"google", "gemini-3.8-flash", WireChat},
	}
	for _, c := range cases {
		if got := SelectWire(spec(c.provider), c.model); got != c.want {
			t.Errorf("SelectWire(%s, %s) = %q, want %q", c.provider, c.model, got, c.want)
		}
	}
}
