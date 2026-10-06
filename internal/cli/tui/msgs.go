package tui

import (
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
)

// This file defines the tea.Msg types the event bridge (bridge.go) produces from
// a run's AgentEvents (US-004, SPEC 5.1). Each raw runtime signal is converted to
// exactly one of these value types so the Bubble Tea Update loop can dispatch on
// them with a plain type switch, keeping all run-time state changes on the tea
// goroutine (node #388 wires them into Model.Update). Every type is a value (not
// a pointer) so it flows through the tea.Msg (any) channel without aliasing the
// producer goroutine's state.

// textDeltaMsg carries the newest suffix of streaming assistant text — the bytes
// produced since the previous delta for the current turn (see DrainStream's
// OnText contract).
type textDeltaMsg struct{ delta string }

// streamRenderTickMsg fires when the streaming render coalescing window
// elapses: the model lays out every delta accumulated since the tick was armed
// in one reflow (see Model.streamRenderArmed).
type streamRenderTickMsg struct{}

// selScrollTickMsg is the edge-autoscroll heartbeat while a mouse text-selection
// drag is pinned at the transcript's top/bottom edge: each tick scrolls a few
// lines so the selection can extend across pages. gen guards stale ticks from
// an earlier drag (a new press bumps Model.selScrollGen and orphans them).
type selScrollTickMsg struct{ gen int }

// turnEndMsg fires once per completed turn with the final assistant message and
// the tool results produced during it.
type turnEndMsg struct {
	msg     agentcore.AssistantMessage
	results []agentcore.ToolResultMessage
}

// toolAnnounceMsg is emitted when a tool call first appears in a streaming
// partial, before it executes: the TUI opens its card live (codex Running
// order) instead of waiting for the execution phase. The later toolStartMsg
// for the same id completes setup without adding a second block.
type toolAnnounceMsg struct {
	id    string
	name  string
	input map[string]any
}

// toolStartMsg is emitted before a tool runs. input holds the decoded call
// arguments when they are a JSON object; it is nil otherwise (the raw Args are
// an untyped any at the event layer).
type toolStartMsg struct {
	id    string
	name  string
	input map[string]any
}

// toolUpdateMsg carries a partial result streamed during a tool's execution.
type toolUpdateMsg struct {
	id      string
	partial string
}

// toolEndMsg is emitted when a tool finishes. ok is false when the tool reported
// an error; result is the tool's textual output; details carries the tool's
// result metadata (Details) when it reported any, e.g. edit's diff.
type toolEndMsg struct {
	id      string
	ok      bool
	result  string
	details any
}

// subagentProgressMsg carries a running sub-agent's structured progress
// (translated from agentcore.SubAgentProgressEvent). id is the parent task
// tool-call id (the row key, matching the task's toolStartMsg/toolEndMsg id);
// desc is the task description (may be empty); activity is the current phase
// ("Reading"/"Editing"/…, never empty); tokens is a coarse output estimate
// (0 = unknown). Elapsed is NOT carried — the model computes it from the row's
// start time so the panel stays live without an event per frame.
type subagentProgressMsg struct {
	id       string
	desc     string
	activity string
	tokens   int
}

// telemetryMsg carries the run's end-of-run telemetry summary.
type telemetryMsg struct{ ev agentcore.TelemetryEvent }

// contextUsageMsg carries a mid-run context-window usage update. Unlike the
// run-end telemetryMsg, it arrives at every turn boundary, so the status bar
// tracks usage while a long multi-tool run is still in flight.
type contextUsageMsg struct {
	tokens int
	window int
}

// compactionStartMsg signals that the loop is about to compact the context
// window. It pins the spinner to a "Compacting conversation…" label while the
// summarization request is in flight; compactionMsg clears it.
type compactionStartMsg struct{}

// compactionMsg signals that the loop compacted the context window. The event's
// details are not needed by the transcript, so it is a bare signal.
type compactionMsg struct{}

// trustPromptMsg asks the model to open the first-run trust picker. It is
// emitted by Init when withSession saw an undecided launch directory, so the
// dialog appears on the Update goroutine before the first prompt.
type trustPromptMsg struct{}

// runEndMsg is the final message: the run has fully drained. err is non-nil when
// the run ended in error (or was interrupted).
type runEndMsg struct{ err error }

// steerInjectedMsg reports that the run loop drained mid-run user input from
// the steer queue and injected it into the conversation (as the next turn's
// input). The TUI echoes each text as a user block at exactly this moment, so
// the transcript shows the message when it actually reaches the model — until
// then it waits in the queued-messages preview above the composer.
type steerInjectedMsg struct{ texts []string }

// judgeNoteMsg carries one permission-gate decision (approval, sandbox
// routing, denial, read-only block) with its rationale, published by the gate
// through the run's event channel so it renders inline, right above the tool
// card it decided.
type judgeNoteMsg struct{ note judge.Note }

// remoteInputMsg carries a prompt submitted from the paired remote browser
// (remote-control, #443). The listener Cmd (Model.waitRemoteInput) blocks on the
// bridge's RemoteInput channel and emits one per submission, re-issued after each
// so successive remote prompts keep arriving.
type remoteInputMsg struct{ text string }
