package provider

import (
	"strings"
	"testing"
)

func TestNormalizeProtocol(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty stays empty", "", "", false},
		{"openai", "openai", ProtocolOpenAI, false},
		{"openai/chat aliases openai", "openai/chat", ProtocolOpenAI, false},
		{"openai/resp_api distinct", "openai/resp_api", ProtocolOpenAIResponses, false},
		{"anthropic unchanged", "anthropic", ProtocolAnthropic, false},
		{"case-insensitive", "OpenAI/Resp_API", ProtocolOpenAIResponses, false},
		{"trimmed", "  openai/chat  ", ProtocolOpenAI, false},
		{"unknown rejected", "openai/foo", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeProtocol(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeProtocol(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeProtocol(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeProtocol(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEffectiveProtocol(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		protocol string
		want     string
	}{
		{"gateway deepseek takes its native Responses wire", "opencode-go", "deepseek-v4.1-flash", "", ProtocolOpenAIResponses},
		{"gateway glm takes its native Responses wire", "opencode-go", "glm-4.7", "", ProtocolOpenAIResponses},
		{"gateway gpt takes its native Responses wire", "opencode-go", "gpt-5.6-luna", "", ProtocolOpenAIResponses},
		{"gateway grok takes its native Responses wire", "opencode-go", "grok-4.7", "", ProtocolOpenAIResponses},
		{"gateway muse takes its native Responses wire", "opencode-go", "muse-spark-1.3", "", ProtocolOpenAIResponses},
		{"gateway claude takes its native Messages wire", "opencode-go", "claude-haiku-5-5", "", ProtocolAnthropic},
		{"gateway gemini takes its native v1beta wire", "opencode-go", "gemini-4-argon-contributor", "", ProtocolGemini},
		{"chat-only gateway translates gemini", "openrouter", "gemini-3.8-flash", "", ProtocolOpenAI},
		{"explicit resp_api wins", "opencode-go", "glm-4.7", "openai/resp_api", ProtocolOpenAIResponses},
		{"explicit chat wins over the native wire", "opencode-go", "claude-haiku-5-5", "openai", ProtocolOpenAI},
		{"anthropic provider serves Messages", "anthropic", "claude-fable-5-1", "", ProtocolAnthropic},
		{"first-party deepseek serves Responses", "deepseek", "deepseek-chat", "", ProtocolOpenAIResponses},
		{"inferred provider from model", "", "deepseek-chat", "", ProtocolOpenAIResponses},
		{"unknown provider falls back to explicit", "nope", "x", "anthropic", ProtocolAnthropic},
		{"unknown provider no protocol", "nope", "x", "", ""},
		{"bad protocol rejected", "opencode-go", "glm-4.7", "carrier-pigeon", ""},
		{"conflicting pair rejected", "anthropic", "claude-fable-5-1", "openai", ""},
		{"declared wire variant allowed", "opencode-go", "claude-haiku-5-5", "anthropic", ProtocolAnthropic},
		{"gemini selector accepted on the gateway", "opencode-go", "gemini-4-argon-contributor", "gemini", ProtocolGemini},
		{"undeclared variant rejected", "deepseek", "deepseek-chat", "anthropic", ""},
		{"renamed alias resolves to the model's native wire", "opencode", "glm-4.7", "", ProtocolOpenAIResponses},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveProtocol(tc.provider, tc.model, tc.protocol); got != tc.want {
				t.Errorf("EffectiveProtocol(%q, %q, %q) = %q, want %q",
					tc.provider, tc.model, tc.protocol, got, tc.want)
			}
		})
	}
}

// The rejection message must name every accepted value so a user with a typo
// can self-correct without reading source.
func TestNormalizeProtocolErrorNamesAcceptedValues(t *testing.T) {
	_, err := NormalizeProtocol("openai/foo")
	if err == nil {
		t.Fatal("expected error for unknown protocol")
	}
	for _, want := range []string{"openai", "openai/chat", "openai/resp_api", "anthropic"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing accepted value %q", err.Error(), want)
		}
	}
}

// TestAllowsProtocol pins the one rule behind both the resolver's conflict
// error and the display path's protocol label: a provider's own protocol, the
// Responses upgrade an OpenAI-wired gateway is assumed to serve, and any wire
// variant it declares are legal; everything else — including the Messages wire
// on a gateway that has not declared it — is not.
func TestAllowsProtocol(t *testing.T) {
	opencodeGo, ok := LookupProviderSpec("opencode-go")
	if !ok {
		t.Fatal("opencode-go spec missing")
	}
	deepseek, ok := LookupProviderSpec("deepseek")
	if !ok {
		t.Fatal("deepseek spec missing")
	}
	anthropic, ok := LookupProviderSpec("anthropic")
	if !ok {
		t.Fatal("anthropic spec missing")
	}
	cases := []struct {
		name string
		spec ProviderSpec
		in   string
		want bool
	}{
		{"registered protocol", opencodeGo, ProtocolOpenAI, true},
		{"responses upgrade on an OpenAI-wired gateway", opencodeGo, ProtocolOpenAIResponses, true},
		{"declared wire variant", opencodeGo, ProtocolAnthropic, true},
		{"undeclared Messages wire", deepseek, ProtocolAnthropic, false},
		{"registered protocol on an Anthropic-wired gateway", anthropic, ProtocolAnthropic, true},
		{"responses upgrade does not apply to it", anthropic, ProtocolOpenAIResponses, false},
		{"empty is not a protocol", opencodeGo, "", false},
	}
	for _, tc := range cases {
		if got := AllowsProtocol(tc.spec, tc.in); got != tc.want {
			t.Errorf("%s: AllowsProtocol(%s, %q) = %v, want %v", tc.name, tc.spec.Name, tc.in, got, tc.want)
		}
	}
	// The conflict message names every value the provider accepts, so a user who
	// typed the wrong wire can self-correct.
	choices := ProtocolChoices(opencodeGo)
	for _, want := range []string{`"openai"`, `"openai/resp_api"`, `"anthropic"`} {
		if !strings.Contains(choices, want) {
			t.Errorf("ProtocolChoices(opencode-go) = %q, want it to name %s", choices, want)
		}
	}
}
