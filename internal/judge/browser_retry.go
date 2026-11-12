package judge

import (
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

type browserMutationIdentity struct {
	Action   string `json:"action"`
	Browser  string `json:"browser"`
	Tab      string `json:"tab"`
	TargetID string `json:"target_id"`
	PageURL  string `json:"page_url"`
	Ref      string `json:"ref"`
	Selector string `json:"selector"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Text     string `json:"text"`
	Value    string `json:"value"`
	Key      string `json:"key"`
	Exact    bool   `json:"exact"`
}

func browserSameMutation(a, b browserMutationIdentity) bool {
	refA, refB := strings.TrimPrefix(strings.TrimSpace(a.Ref), "@"), strings.TrimPrefix(strings.TrimSpace(b.Ref), "@")
	if a.Action != b.Action || refA != refB || strings.TrimSpace(a.Selector) != strings.TrimSpace(b.Selector) || a.Role != b.Role || a.Name != b.Name ||
		a.Text != b.Text || a.Value != b.Value || a.Key != b.Key || a.Exact != b.Exact {
		return false
	}
	// A prior model call can omit host-prepared fields. Missing is not evidence
	// that the same-looking operation was on a different browser or tab.
	for _, pair := range [][2]string{{a.Browser, b.Browser}, {a.Tab, b.Tab}, {a.TargetID, b.TargetID}, {a.PageURL, b.PageURL}} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return false
		}
	}
	return true
}

// priorBrowserUnknown matches a host-reported unknown outcome to the original
// tool call. Page text, model claims, and a retry's arguments cannot assert it.
func priorBrowserUnknown(call agentcore.AgentToolCall, msgs agentcore.MessageList) bool {
	if !strings.EqualFold(call.Name, "browser") {
		return false
	}
	var current browserMutationIdentity
	if json.Unmarshal(call.Arguments, &current) != nil {
		return false
	}
	switch current.Action {
	case "click", "dblclick", "check", "uncheck", "fill", "type", "select", "press", "insert", "dialog_accept":
	default:
		return false
	}
	results := make(map[string]agentcore.ToolResultMessage)
	for i := len(msgs) - 1; i >= 0; i-- {
		switch msg := msgs[i].(type) {
		case agentcore.CompactionMessage:
			return false // earlier detailed call/result pairs are no longer available
		case agentcore.ToolResultMessage:
			if strings.EqualFold(msg.ToolName, "browser") && msg.ToolCallID != "" {
				results[msg.ToolCallID] = msg
			}
		case agentcore.AssistantMessage:
			for _, prev := range msg.ToolCalls() {
				result, ok := results[prev.ID]
				if !ok || !strings.EqualFold(prev.Name, "browser") || prev.IsServer() {
					continue
				}
				var prior browserMutationIdentity
				if json.Unmarshal(prev.Arguments, &prior) != nil || !browserSameMutation(current, prior) {
					continue
				}
				var state struct {
					ExecutionState string `json:"execution_state"`
				}
				data, err := json.Marshal(result.Details)
				return result.IsError && err == nil && json.Unmarshal(data, &state) == nil && state.ExecutionState == "unknown"
			}
		}
	}
	return false
}
