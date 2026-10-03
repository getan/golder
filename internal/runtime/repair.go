package runtime

import (
	"fmt"
	"os"

	"github.com/getan/golder/internal/agentcore"
)

// repairDanglingToolCalls answers every assistant tool call in the context
// that has no matching tool result, and reports how many it fixed.
//
// The provider protocol requires each function call to be followed by its
// output: a resumed session whose history ends with an unanswered call (a run
// that was interrupted between the assistant message landing in the context
// and the tools executing — the old tool-turn cap did exactly this) is
// rejected with "No tool output found for function call …", so the first
// prompt after /resume fails before the model ever sees it.
//
// The repair runs at the head of every run: it is idempotent, and the
// synthetic results become part of the context, so the next persist heals the
// session file itself rather than patching every request forever.
func repairDanglingToolCalls(agentCtx *agentcore.AgentContext) int {
	answered := make(map[string]bool)
	for _, m := range agentCtx.Messages {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID != "" {
			answered[tr.ToolCallID] = true
		}
	}

	repaired := 0
	out := make(agentcore.MessageList, 0, len(agentCtx.Messages))
	for _, m := range agentCtx.Messages {
		out = append(out, m)
		am, ok := m.(agentcore.AssistantMessage)
		if !ok {
			continue
		}
		for _, c := range am.ToolCalls() {
			// Hosted (server-executed) calls ran provider-side and never get a
			// local result; an empty id cannot be paired at all.
			if c.ID == "" || c.IsServer() || answered[c.ID] {
				continue
			}
			answered[c.ID] = true
			repaired++
			out = append(out, agentcore.ToolResultMessage{
				RoleField:  agentcore.RoleToolResult,
				ToolCallID: c.ID,
				ToolName:   c.Name,
				Content: agentcore.ContentList{agentcore.NewTextContent(
					"The previous run ended before this tool call ran (it was interrupted, or the run stopped mid-turn), " +
						"so there is no output for it. Re-issue the call if you still need its result.")},
				IsError: true,
			})
		}
	}
	if repaired > 0 {
		agentCtx.Messages = out
	}
	return repaired
}

// healResumedContext repairs a context loaded from disk and tells the user,
// so a session saved by an interrupted run resumes cleanly instead of failing
// the provider request.
func healResumedContext(agentCtx *agentcore.AgentContext) {
	if n := repairDanglingToolCalls(agentCtx); n > 0 {
		fmt.Fprintf(os.Stderr, "golder: repaired %d unanswered tool call(s) from an earlier run\n", n)
	}
}
