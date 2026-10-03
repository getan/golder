package runtime

import (
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// Server-executed calls must never reach the local batch (no "unknown tool"
// failures); local calls pass through untouched.
func TestToAgentToolCallsFiltersServer(t *testing.T) {
	blocks := []agentcore.ToolCallContent{
		agentcore.NewToolCallContent("c1", "read", json.RawMessage(`{}`)),
		agentcore.NewServerToolCallContent("ws1", "web_search", json.RawMessage(`{"query":"x"}`)),
	}
	calls := toAgentToolCalls(blocks)
	if len(calls) != 1 || calls[0].ID != "c1" {
		t.Errorf("toAgentToolCalls = %+v, want only the local call", calls)
	}
	if got := serverToolCalls(blocks); len(got) != 1 || got[0].ID != "ws1" {
		t.Errorf("serverToolCalls = %+v, want only the server call", got)
	}
	if got := serverCallSummary(blocks[1]); got != "web_search(x)" {
		t.Errorf("serverCallSummary = %q, want %q", got, "web_search(x)")
	}
	if got := serverCallSummary(agentcore.NewServerToolCallContent("w", "web_search", nil)); got != "web_search" {
		t.Errorf("serverCallSummary without args = %q, want bare name", got)
	}
}
