package provider

import (
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// TestWireReasoningEffort pins the unified mapping table: each family's ladder,
// both ends of the clamp, and the off/omit behavior.
func TestWireReasoningEffort(t *testing.T) {
	cases := []struct {
		name  string
		model string
		level agentcore.ThinkingLevel
		want  string
	}{
		// DeepSeek keeps the full ladder, max included.
		{"deepseek max", "deepseek-v4.1-flash", agentcore.ThinkingMax, "max"},
		{"deepseek xhigh", "deepseek-v4.1-flash", agentcore.ThinkingXHigh, "xhigh"},
		{"deepseek minimal", "deepseek/deepseek-v4-pro", agentcore.ThinkingMinimal, "minimal"},
		{"deepseek case-insensitive", "DeepSeek-V4.1-Flash", agentcore.ThinkingMax, "max"},
		// Muse caps at xhigh: it advertises max but rejects it on the wire.
		{"muse max clamps down", "muse-spark-1.3-contributor", agentcore.ThinkingMax, "xhigh"},
		{"muse xhigh", "muse-spark-1.3-contributor", agentcore.ThinkingXHigh, "xhigh"},
		// Unknown families get the conservative low/medium/high ladder.
		{"default max clamps down", "gpt-4o", agentcore.ThinkingMax, "high"},
		{"default xhigh clamps down", "qwen-max", agentcore.ThinkingXHigh, "high"},
		{"default minimal clamps up", "gpt-4o", agentcore.ThinkingMinimal, "low"},
		{"default medium", "gpt-4o", agentcore.ThinkingMedium, "medium"},
		{"empty model id", "", agentcore.ThinkingHigh, "high"},
		// off/unset omit the field so the gateway keeps its default.
		{"off", "deepseek-v4.1-flash", agentcore.ThinkingOff, ""},
		{"unset", "deepseek-v4.1-flash", "", ""},
	}
	for _, c := range cases {
		if got := WireReasoningEffort(c.model, c.level); got != c.want {
			t.Errorf("%s: WireReasoningEffort(%q, %q) = %q, want %q", c.name, c.model, c.level, got, c.want)
		}
	}
}

// TestReasoningWiresAgree pins the requirement that Chat Completions and
// Responses carry the same effort for the same model and level: both encoders
// read the one table, so neither can drift from the other.
func TestReasoningWiresAgree(t *testing.T) {
	models := []string{"deepseek-v4.1-flash", "muse-spark-1.3-contributor", "gpt-4o", "qwen-max"}
	levels := []agentcore.ThinkingLevel{
		agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
		agentcore.ThinkingMax,
	}
	for _, model := range models {
		for _, level := range levels {
			req := CompletionRequest{Model: model, Config: StreamConfig{ThinkingLevel: level}}

			chat := ""
			b, err := encodeOpenAIRequest(req)
			if err != nil {
				t.Fatalf("encode chat (%s, %s): %v", model, level, err)
			}
			if raw, ok := decodeBody(t, b)["reasoning_effort"]; ok {
				chat, _ = raw.(string)
			}

			responses := buildResponsesParams(req, false)
			wire := string(responses.Reasoning.Effort)

			if chat != wire {
				t.Errorf("%s/%s: chat sends %q but responses sends %q", model, level, chat, wire)
			}
			if want := WireReasoningEffort(model, level); wire != want {
				t.Errorf("%s/%s: wire %q, table says %q", model, level, wire, want)
			}
		}
	}
}
