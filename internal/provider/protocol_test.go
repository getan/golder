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

func TestPreferResponses(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"grok-4", true},
		{"x-ai/grok-2-1212", true},
		{"muse-spark-1.3", true},
		{"MUSE-SPARK-1.3-CONTRIBUTOR", true},
		{"deepseek-chat", true},
		{"deepseek/deepseek-r1", true},
		{"gpt-4o", true},
		{"GPT-4o-mini", true},
		{"o3", true},
		{"o4-mini", true},
		{"claude-opus-4-8", false},
		{"anthropic/claude-3.5-sonnet", false},
		{"qwen-max", false},
		{"meta/llama-3.3-70b-instruct", false},
		{"", false},
	}
	for _, c := range cases {
		if got := PreferResponses(c.id); got != c.want {
			t.Errorf("PreferResponses(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
