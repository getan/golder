package judge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestGUITerminalApprovalSummaryIncludesExactTargetAndInput(t *testing.T) {
	input := strings.Repeat("a", 300) + "END"
	raw, _ := json.Marshal(map[string]string{"action": "send", "pane": "terminal-42", "text": input})
	got := riskSummary(agentcore.AgentToolCall{Name: "terminal", Arguments: raw})
	if got != string(raw) || !strings.Contains(got, "terminal-42") || !strings.Contains(got, "END") {
		t.Fatalf("terminal approval summary omitted target or input: %q", got)
	}
}
