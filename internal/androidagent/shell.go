package androidagent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
)

// shellTool retains the existing bash execution and gate protocol but does not
// advertise desktop-only sandbox or network-isolation guarantees on Android.
type shellTool struct{ tool *agenttool.BashTool }

func (t *shellTool) Name() string { return "bash" }

func (t *shellTool) Description() string {
	return "Run a POSIX shell command in the selected working directory after host authorization. " +
		"Android App isolation is not a workspace-level shell sandbox; a command may reach other " +
		"data and network accessible to the App. A missing per-call authorization fails closed. " +
		"timeout_ms sets a hard deadline. A running command returns bash_id for write_stdin."
}

func (t *shellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "command":{"type":"string","description":"POSIX shell command to run."},
    "timeout_ms":{"type":"integer","minimum":0,"description":"Hard deadline in milliseconds."},
    "yield_time_ms":{"type":"integer","minimum":0,"description":"Initial wait before returning a session id."},
    "network":{"type":"string","enum":["on"],"description":"Request network capability from the host gate. Android App isolation cannot enforce network=off per command."}
  },
  "required":["command"],
  "additionalProperties":false
}`)
}

func (t *shellTool) ExecutionMode() agentcore.ToolExecutionMode {
	return t.tool.ExecutionMode()
}

func (t *shellTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var request struct {
		Network string `json:"network"`
	}
	if json.Unmarshal(args, &request) == nil && strings.EqualFold(strings.TrimSpace(request.Network), "off") {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent("bash: Android cannot enforce network=off for an unconfined shell; command was not run")},
			IsError: true,
		}, nil
	}
	return t.tool.Execute(ctx, id, args, onUpdate)
}
