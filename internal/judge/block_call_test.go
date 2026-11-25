package judge

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestBlockCallMarksDecisionOneShot(t *testing.T) {
	dec := blockCall(
		agentcore.AgentToolCall{Name: "bash"},
		Verdict{Level: Deny, Reasons: []string{"test refusal"}},
		"try another shape",
	)
	if dec == nil || dec.Content == nil || len(*dec.Content) != 1 {
		t.Fatalf("unexpected block decision: %+v", dec)
	}
	text, ok := (*dec.Content)[0].(agentcore.TextContent)
	if !ok {
		t.Fatalf("block content = %T, want agentcore.TextContent", (*dec.Content)[0])
	}
	lower := strings.ToLower(text.Text)
	for _, want := range []string{
		"one-shot decision",
		"not a permanent ban",
		"decided fresh",
		"try another shape",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("block message missing %q: %s", want, text.Text)
		}
	}
}
