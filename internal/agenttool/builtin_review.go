package agenttool

import "github.com/getan/golder/internal/agentcore"

// Only concrete in-process implementations are eligible for first-party
// classification. The classifier still checks the operation: write_stdin can
// send input, whereas an empty poll only reads an existing session. Names and
// tool-provided interfaces cannot attest a plugin as one of these implementations.
func builtinReviewName(tool agentcore.AgentTool) string {
	switch t := tool.(type) {
	case *TodoTool:
		if t != nil {
			return "todo"
		}
	case *GetContextRemainingTool:
		if t != nil {
			return "get_context_remaining"
		}
	case *scheduleListTool:
		if t != nil {
			return "schedule_list"
		}
	case *WriteStdinTool:
		if t != nil {
			return "write_stdin"
		}
	case *BrowserTool:
		if t != nil {
			return "browser"
		}
	case *ComputerTool:
		if t != nil {
			return "computer"
		}
	}
	return ""
}
