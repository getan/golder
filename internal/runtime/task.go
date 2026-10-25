// This file implements the generic `task` tool (US-002, #454): a general-purpose
// sub-agent the model can dispatch with a free-form prompt to fan out work in a
// single assistant message. It is a thin specialization of SubAgentTool - it
// reuses executeGoroutine's child-loop driving - configured with a generic
// system prompt (the delegated task comes from the call arguments at runtime),
// a shared concurrency semaphore, and a child tool set from which `task` itself
// is excluded (the nesting guard, wired in internal/cli/run).
package runtime

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// DefaultMaxSubagents is the concurrency cap applied when GOLDER_MAX_SUBAGENTS is
// unset or invalid. It bounds how many task sub-agents run at once so a fan-out
// cannot overwhelm the provider rate limit.
const DefaultMaxSubagents = 4

// taskDescription uses the same delegation policy as the main system prompt so
// the tool description cannot retain a stale authorization rule.
const taskDescription = "The task tool dispatches a general-purpose sub-agent for one concrete, bounded task. " +
	taskGuide

// taskSystemPrompt seeds every generic sub-agent's context. It is intentionally
// generic (the actual work arrives as the runtime prompt) and mirrors the
// parent agent's operating posture so a delegated task is carried out the same
// way the parent would.
const taskSystemPrompt = "You are a focused sub-agent working on one delegated task. " +
	"You have your own context and the standard tool set, but you cannot spawn further sub-agents. " +
	"Follow the inherited instructions and the delegated task's constraints. When editing, stay within " +
	"your assigned files, re-read their current contents, and preserve unrelated changes made by others. " +
	"Complete and validate the task using the tools available; report blockers rather than silently " +
	"expanding your scope. Respond with a concise final report of your findings, changed files, tests " +
	"run and any remaining issues. " +
	"Your final message is returned verbatim to the agent that dispatched you, so make it self-contained."

// taskSchema is the JSON Schema for a task invocation: a required self-contained
// prompt plus an optional short description used for status display.
var taskSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "A short (3-5 word) description of the task, for status display."
    },
    "prompt": {
      "type": "string",
      "description": "The delegated task: goal, relevant context, constraints, file ownership if editing, and acceptance checks. Even a fork needs a clear task statement."
    },
    "context": {
      "type": "string",
      "enum": ["fresh", "fork"],
      "description": "Context mode. fresh (default) starts a clean child with the project instructions and git status but none of this conversation; use it for independent fan-out. fork inherits this conversation and system prompt; use it when the task continues work already discussed here."
    }
  },
  "required": ["prompt"],
  "additionalProperties": false
}`)

// NewTaskTool builds the generic `task` sub-agent tool. factory produces a fresh
// child RunConfig per spawn (reusing the parent's provider stream/model and a
// child tool registry that must exclude `task` for the nesting guard); sem is a
// shared buffered channel bounding concurrent task runs (nil disables limiting).
// The child prompt comes from the call arguments at runtime, so a single generic
// spec serves every delegated task.
func NewTaskTool(factory func() RunConfig, sem chan struct{}) *SubAgentTool {
	return NewTaskToolWithOptions(factory, sem)
}

// TaskOption customizes the generic task tool. It exists so the common two-
// argument constructor stays source-compatible for tests and embedders while
// the CLI can supply the project directory used for the fresh/fork brief.
type TaskOption func(*SubAgentSpec)

// WithTaskProjectDir names the working directory whose git status is included
// in a task child's project brief.
func WithTaskProjectDir(dir string) TaskOption {
	return func(spec *SubAgentSpec) { spec.ProjectDir = dir }
}

// NewTaskToolWithOptions builds the generic task tool with optional project
// context wiring.
func NewTaskToolWithOptions(factory func() RunConfig, sem chan struct{}, opts ...TaskOption) *SubAgentTool {
	spec := SubAgentSpec{
		Name:           "task",
		Description:    taskDescription,
		SystemPrompt:   taskSystemPrompt,
		Schema:         taskSchema,
		NewRunConfig:   factory,
		Sem:            sem,
		ProjectContext: true,
	}
	for _, opt := range opts {
		opt(&spec)
	}
	return NewSubAgentTool(spec)
}

// MaxSubagents resolves the concurrency cap for task sub-agents from
// GOLDER_MAX_SUBAGENTS: absent or unparseable yields DefaultMaxSubagents (4), and
// a parsed value below 1 is floored to 1 so the semaphore always admits at least
// one runner.
func MaxSubagents() int {
	v := strings.TrimSpace(os.Getenv("GOLDER_MAX_SUBAGENTS"))
	if v == "" {
		return DefaultMaxSubagents
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultMaxSubagents
	}
	if n < 1 {
		return 1
	}
	return n
}

// NewSubagentSemaphore builds the shared concurrency semaphore for task
// sub-agents, sized by MaxSubagents. One instance per run is created and shared
// across every task call so the cap is enforced run-wide.
func NewSubagentSemaphore() chan struct{} {
	return make(chan struct{}, MaxSubagents())
}

// activityOf maps a child sub-agent event to the display verb surfaced in a
// SubAgentProgressEvent (D-8: tool name / phase, no argument summary). A child
// ToolExecutionStartEvent maps by tool name; a TurnStartEvent (a fresh turn with
// no tool in progress) maps to "Thinking". Every other event maps to "" so the
// caller emits nothing — progress is reported only at these activity boundaries,
// keeping event volume proportional to the child's tool calls rather than its
// text deltas (D-7).
func activityOf(ev agentcore.AgentEvent) string {
	switch e := ev.(type) {
	case agentcore.ToolExecutionStartEvent:
		switch e.ToolName {
		case "read":
			return "Reading"
		case "apply_patch":
			return "Editing"
		case "bash":
			return "Running bash"
		case "grep", "find", "ls":
			return "Searching"
		case "webfetch":
			return "Fetching"
		default:
			return ""
		}
	case agentcore.TurnStartEvent:
		return "Thinking"
	default:
		return ""
	}
}

// estimateTokens gives a coarse output-token estimate from a running character
// count of the child's streamed text (~4 chars per token). It rides along on
// each progress event as a rough "↓ tokens" figure; 0 means unknown (no text
// streamed yet).
func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return chars / 4
}
