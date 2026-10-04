package provider

import (
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// TestWireReasoningEffortFallback pins the family-table fallback used when the
// models.dev catalog has nothing for the model: each family's ladder, both ends
// of the clamp, and the off/omit behavior.
func TestWireReasoningEffortFallback(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		level    agentcore.ThinkingLevel
		want     string
	}{
		// DeepSeek's family fallback keeps the full ladder: with no catalog
		// entry (cold cache, unknown model) the requested rung passes through
		// unclamped. The catalog clamps once fetched (TestWireReasoningEffortCatalog).
		{"deepseek max", "opencode-go", "deepseek-v4.1-flash", agentcore.ThinkingMax, "max"},
		{"deepseek xhigh", "opencode-go", "deepseek-v4.1-flash", agentcore.ThinkingXHigh, "xhigh"},
		{"deepseek minimal", "opencode-go", "deepseek/deepseek-v4-pro", agentcore.ThinkingMinimal, "minimal"},
		{"deepseek case-insensitive", "opencode-go", "DeepSeek-V4.1-Flash", agentcore.ThinkingMax, "max"},
		// Muse caps at xhigh: it advertises max but rejects it on the wire.
		{"muse max clamps down", "opencode-go", "muse-spark-1.3-contributor", agentcore.ThinkingMax, "xhigh"},
		{"muse xhigh", "opencode-go", "muse-spark-1.3-contributor", agentcore.ThinkingXHigh, "xhigh"},
		// Unknown families get the conservative low/medium/high ladder.
		{"default max clamps down", "openai", "gpt-4o", agentcore.ThinkingMax, "high"},
		{"default xhigh clamps down", "openai", "qwen-max", agentcore.ThinkingXHigh, "high"},
		{"default minimal clamps up", "openai", "gpt-4o", agentcore.ThinkingMinimal, "low"},
		{"default medium", "openai", "gpt-4o", agentcore.ThinkingMedium, "medium"},
		{"empty model id", "openai", "", agentcore.ThinkingHigh, "high"},
		{"empty provider id", "", "gpt-4o", agentcore.ThinkingLow, "low"},
		// off/unset omit the field so the gateway keeps its default.
		{"off", "opencode-go", "deepseek-v4.1-flash", agentcore.ThinkingOff, ""},
		{"unset", "opencode-go", "deepseek-v4.1-flash", "", ""},
	}
	for _, c := range cases {
		if got := WireReasoningEffort(c.provider, c.model, c.level); got != c.want {
			t.Errorf("%s: WireReasoningEffort(%q, %q, %q) = %q, want %q", c.name, c.provider, c.model, c.level, got, c.want)
		}
	}
}

// TestWireReasoningEffortCatalog pins catalog precedence: the models.dev entry
// beats the family table for both clamping and suppression, and models the
// catalog does not know still fall back.
func TestWireReasoningEffortCatalog(t *testing.T) {
	setReasoningCatalogForTest(t, map[string]map[string]reasoningEntry{
		"opencode-go": {
			"glm-5.3":    {Reasoning: true, Levels: []string{"low", "high", "max"}},
			"kimi-k3":    {Reasoning: true, Levels: []string{"max"}},
			"no-think-1": {Reasoning: false},
			"toggle-1":   {Reasoning: true},
		},
	})
	cases := []struct {
		name     string
		provider string
		model    string
		level    agentcore.ThinkingLevel
		want     string
	}{
		{"catalog exact", "opencode-go", "glm-5.3", agentcore.ThinkingHigh, "high"},
		{"catalog top", "opencode-go", "glm-5.3", agentcore.ThinkingMax, "max"},
		{"catalog clamps down", "opencode-go", "glm-5.3", agentcore.ThinkingXHigh, "high"},
		{"catalog clamps up", "opencode-go", "glm-5.3", agentcore.ThinkingMinimal, "low"},
		{"catalog single rung", "opencode-go", "kimi-k3", agentcore.ThinkingLow, "max"},
		{"catalog non-reasoning suppresses", "opencode-go", "no-think-1", agentcore.ThinkingHigh, ""},
		// The catalog lists no effort values, so the family table still applies.
		{"catalog without levels falls back", "opencode-go", "toggle-1", agentcore.ThinkingMedium, "medium"},
		// Another provider's entries never leak in.
		{"other provider falls back", "openai", "glm-5.3", agentcore.ThinkingMax, "high"},
	}
	for _, c := range cases {
		if got := WireReasoningEffort(c.provider, c.model, c.level); got != c.want {
			t.Errorf("%s: WireReasoningEffort(%q, %q, %q) = %q, want %q", c.name, c.provider, c.model, c.level, got, c.want)
		}
	}
}

// TestReasoningWiresAgree pins the requirement that Chat Completions and
// Responses carry the same effort for the same provider/model/level: both
// encoders read the one table, so neither can drift from the other.
func TestReasoningWiresAgree(t *testing.T) {
	setReasoningCatalogForTest(t, map[string]map[string]reasoningEntry{
		"opencode-go": {
			"glm-5.3": {Reasoning: true, Levels: []string{"low", "high", "max"}},
		},
	})
	models := []string{"deepseek-v4.1-flash", "muse-spark-1.3-contributor", "gpt-4o", "qwen-max", "glm-5.3"}
	levels := []agentcore.ThinkingLevel{
		agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
		agentcore.ThinkingMax,
	}
	for _, model := range models {
		for _, level := range levels {
			req := CompletionRequest{Model: model, Config: StreamConfig{ThinkingLevel: level}}

			chat := ""
			b, err := encodeOpenAIRequest("opencode-go", req)
			if err != nil {
				t.Fatalf("encode chat (%s, %s): %v", model, level, err)
			}
			if raw, ok := decodeBody(t, b)["reasoning_effort"]; ok {
				chat, _ = raw.(string)
			}

			responses := buildResponsesParams("opencode-go", req, false)
			wire := string(responses.Reasoning.Effort)

			if chat != wire {
				t.Errorf("%s/%s: chat sends %q but responses sends %q", model, level, chat, wire)
			}
			if want := WireReasoningEffort("opencode-go", model, level); wire != want {
				t.Errorf("%s/%s: wire %q, table says %q", model, level, wire, want)
			}
		}
	}
}
