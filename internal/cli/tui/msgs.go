package tui

import (
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/runtime"
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

// tokenDeltaMsg carries n characters of model output that feed the spinner's
// token estimate but never the transcript: reasoning text and tool-call
// argument JSON. The bridge measures them against the cumulative stream partial
// (growth only); visible text flows through textDeltaMsg instead.
type tokenDeltaMsg struct{ chars int }

// streamRenderTickMsg fires when the streaming render coalescing window
// elapses: the model lays out every delta accumulated since the tick was armed
// in one reflow (see Model.streamRenderArmed).
type streamRenderTickMsg struct{}

// selScrollTickMsg is the edge-autoscroll heartbeat while a mouse text-selection
// drag is pinned at the transcript's top/bottom edge: each tick scrolls a few
// lines so the selection can extend across pages. gen guards stale ticks from
// an earlier drag (a new press bumps Model.selScrollGen and orphans them).
type selScrollTickMsg struct{ gen int }

// seedMoreMsg is one step of the lazy seed's continuation: a scroll into
// deferred history materializes under a budget (lazySeedBudget), and when that
// budget — not the end of the history — is what stopped the step, the model keeps
// stepping on a short tick until a screen of fresh history sits above the
// position the user stopped at. gen guards stale ticks from a superseded chain
// (scrolling back to the live tail, a session switch, a fresh arm).
type seedMoreMsg struct{ gen int }

// statusRevealMsg advances the status bar's change reveal by one column: the
// bounded pass a long value makes right after it changes, before it settles onto
// its still, elided form. Nothing else on the bar schedules a timer.
type statusRevealMsg struct{}

// turnEndMsg fires once per completed turn with the final assistant message and
// the tool results produced during it.
type turnEndMsg struct {
	msg     agentcore.AssistantMessage
	results []agentcore.ToolResultMessage
	// stall reports a turn that produced nothing the person can act on, and what
	// the loop did about it (see runtime.TurnStall): a reasoning-only turn is
	// silent, so the transcript has to say whether the run is carrying on by
	// itself or has stopped.
	stall runtime.TurnStall
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

// compactionMsg signals that the loop finished a compaction attempt: the
// figures the loop reported, so the announcement can say what happened to the
// conversation rather than only that something did.
//
// err is the failure text when summarization failed — the context was left
// unchanged in that case, and a front-end that claims the context was compacted
// either way makes a broken compaction indistinguishable from a working one.
type compactionMsg struct {
	err string
	// reason is why compaction ran: "threshold" (auto), "overflow", "manual".
	reason string
	// tokensBefore/After are the context sizes either side of the rewrite.
	tokensBefore, tokensAfter int
	// summarized is the message count folded into the summary; kept is the count
	// retained verbatim.
	summarized, kept int
}

// providerNoticeMsg carries a provider-layer progress note that is not message
// content: today, a cut stream that is being retried. It is rendered as a
// harness notice so the wait has an explanation instead of only a spinner.
type providerNoticeMsg struct{ ev agentcore.ProviderNoticeEvent }

// trustPromptMsg explicitly opens the trust picker on the Update goroutine.
// Startup does not emit it: withSession must arm the initial dialog before
// terminal input can race with asynchronous Init commands.
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
