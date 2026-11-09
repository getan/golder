package appsession

import (
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestSeedRestoresToolExecutionEnvelope(t *testing.T) {
	c := New(Options{})
	details := map[string]any{"envelope": "sandbox"}
	c.seed([]agentcore.Message{
		agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewToolCallContent("call", "read", json.RawMessage(`{"path":"a.go"}`))}},
		agentcore.ToolResultMessage{ToolCallID: "call", ToolName: "read", Details: details, Content: agentcore.ContentList{agentcore.NewTextContent("contents")}},
	})
	items := c.Snapshot().Items
	if len(items) != 1 || items[0].Tool == nil || items[0].Tool.Details["envelope"] != "sandbox" {
		t.Fatalf("missing restored tool details: %+v", items)
	}
	details["envelope"] = "bare"
	if c.Snapshot().Items[0].Tool.Details["envelope"] != "sandbox" {
		t.Fatal("restored details share the original map")
	}
}
