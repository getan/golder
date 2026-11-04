package judge

import (
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// priorPushRetryEvidence connects a previous tool call with its actual tool
// result, rather than inferring the outcome from model-generated prose. It is
// evidence for the reviewer, not an authorization or an execution binding:
// persisted conversation data may be incomplete or imported.
func priorPushRetryEvidence(tool string, args json.RawMessage, cwd string, msgs agentcore.MessageList) string {
	if !strings.EqualFold(strings.TrimSpace(tool), "bash") || !escalationRequested(args) {
		return ""
	}
	var current struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &current) != nil {
		return ""
	}
	if _, ok := parseSingleGitPush(current.Command, cwd); !ok {
		return ""
	}
	results := make(map[string]agentcore.ToolResultMessage)
	for i := len(msgs) - 1; i >= 0; i-- {
		switch msg := msgs[i].(type) {
		case agentcore.UserMessage, agentcore.CompactionMessage:
			// A new user turn or compaction may change the request and removes
			// the complete immediate-attempt evidence we need here.
			return ""
		case agentcore.ToolResultMessage:
			if msg.ToolName == "bash" && msg.ToolCallID != "" {
				results[msg.ToolCallID] = msg
			}
		case agentcore.AssistantMessage:
			for _, call := range msg.ToolCalls() {
				result, ok := results[call.ID]
				if !ok || call.Name != "bash" || call.IsServer() {
					continue
				}
				var previous struct {
					Command string `json:"command"`
				}
				if json.Unmarshal(call.Arguments, &previous) != nil || previous.Command != current.Command {
					continue
				}
				// The latest matching attempt controls. A previous escape request,
				// successful call, or missing structured envelope is not a retry.
				if escalationRequested(call.Arguments) || !result.IsError {
					return ""
				}
				details, ok := result.Details.(map[string]any)
				if !ok {
					return ""
				}
				envelope, ok := details["envelope"].(string)
				if !ok || envelope != "sandbox" || !nonzeroExitCode(details["exitCode"]) {
					return ""
				}
				return "prior matching bash call: failed inside the sandbox; this call requests an outside-sandbox retry (conversation evidence only; not a grant)"
			}
		}
	}
	return ""
}

func nonzeroExitCode(value any) bool {
	// Live results use int; JSON-loaded sessions decode numeric values as
	// float64. Other shapes (including missing metadata) do not establish an
	// exit status and must not be interpreted as a failed push.
	switch code := value.(type) {
	case int:
		return code != 0
	case float64:
		return code != 0 && code == float64(int(code))
	default:
		return false
	}
}
