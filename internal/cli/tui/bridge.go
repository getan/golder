package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/runtime"
)

// This file bridges the agent run seam (runtime.StartRun + runtime.DrainStream)
// to Bubble Tea (US-004, SPEC 5.1 bridge / 3.2). The agent loop runs on its own
// goroutine and emits AgentEvents; a Bubble Tea program consumes tea.Msg values
// one at a time from its Update loop. The bridge is a pump: a goroutine drains
// the run and converts every event into the matching tea.Msg (see msgs.go),
// sending it into a buffered channel; a tea.Cmd (waitForEvent) receives one msg
// per Update tick. The channel is the only synchronization point, so the
// producer never touches the model and the model never touches the run — all
// state transitions happen on the tea goroutine.
//
// Back-pressure is intentional: the channel blocks the draining goroutine when
// the buffer is full, so no event is ever dropped (the tea loop always catches
// up). Node #388 wires startRun into Model.Init/Update; this file only provides
// the reusable, unit-testable primitives.

// eventChanCap is the buffer size of the bridge channel. A modest buffer lets a
// burst of tool events queue without blocking the run's goroutine on every send,
// while still bounding memory (blocking, never dropping, past the cap).
const eventChanCap = 64

// newEventChan allocates the buffered channel the bridge pumps run events
// through.
func newEventChan() chan tea.Msg {
	return make(chan tea.Msg, eventChanCap)
}

// newStreamHandler builds the runtime.StreamHandler that converts each run event
// into a tea.Msg and sends it into ch. Sends block when ch is full, applying
// back-pressure to the draining goroutine so no event is lost. It is factored
// out of pump so the callback→msg conversion can be unit-tested without a real
// provider run (see bridge_test.go).
func newStreamHandler(ch chan tea.Msg, extra func(agentcore.AgentEvent)) runtime.StreamHandler {
	// announced tracks tool-call ids already surfaced as live cards this run,
	// so every streaming partial re-listing them stays a no-op. The handler is
	// built per run, so the set never leaks across runs.
	announced := map[string]bool{}
	// lastThink and argsSeen are the spinner's high-water marks (in runes) for
	// the output channels OnText cannot see: thinking text and tool-call
	// argument JSON. Streaming partials are cumulative, so only the growth
	// since the previous partial is new output. Both reset when a message
	// starts, so a provider reusing tool-call ids across turns counts cleanly.
	lastThink := 0
	argsSeen := map[string]int{}
	// accountOutput feeds those channels to the spinner as tokenDeltaMsg. It
	// also runs at turn end so a provider that delivers the whole message in
	// one shot still lands its estimate (mirrors DrainStream's text flush).
	accountOutput := func(am agentcore.AssistantMessage) {
		think := 0
		for _, c := range am.Content {
			if tc, ok := c.(agentcore.ThinkingContent); ok {
				think += len([]rune(tc.Thinking))
			}
		}
		if think > lastThink {
			ch <- tokenDeltaMsg{chars: think - lastThink}
			lastThink = think
		}
		for _, c := range am.ToolCalls() {
			if c.ID == "" {
				continue
			}
			if n := len([]rune(string(c.Arguments))); n > argsSeen[c.ID] {
				ch <- tokenDeltaMsg{chars: n - argsSeen[c.ID]}
				argsSeen[c.ID] = n
			}
		}
	}
	return runtime.StreamHandler{
		OnText: func(delta string) {
			ch <- textDeltaMsg{delta: delta}
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage) {
			accountOutput(msg)
			ch <- turnEndMsg{msg: msg, results: results}
		},
		OnEvent: func(ev agentcore.AgentEvent) {
			// Deliver observer events (plugin notifier, SessionEnd/PreCompact hook)
			// first, then translate into TUI messages.
			if extra != nil {
				extra(ev)
			}
			switch e := ev.(type) {
			case agentcore.MessageStartEvent:
				// A new assistant message begins streaming: reset the spinner's
				// per-message high-water marks.
				lastThink = 0
				argsSeen = map[string]int{}
			case agentcore.MessageUpdateEvent:
				if am, ok := e.Message.(agentcore.AssistantMessage); ok {
					accountOutput(am)
					// Live tool-call announcements (codex Running order): the
					// first partial carrying a call opens its card via
					// toolAnnounceMsg; the executor's later toolStartMsg for
					// the same id only completes setup. Text-only partials
					// need no message (deltas already flow through OnText).
					calls := am.ToolCalls()
					for _, c := range calls {
						if c.ID == "" || announced[c.ID] {
							continue
						}
						announced[c.ID] = true
						ch <- toolAnnounceMsg{id: c.ID, name: c.Name, input: argsToMap(c.Arguments)}
					}
				}
			case agentcore.ToolExecutionStartEvent:
				ch <- toolStartMsg{id: e.ToolCallID, name: e.ToolName, input: argsToMap(e.Args)}
			case agentcore.ToolExecutionUpdateEvent:
				ch <- toolUpdateMsg{id: e.ToolCallID, partial: agentcore.ContentToText(e.PartialResult.Content)}
			case agentcore.ToolExecutionEndEvent:
				ch <- toolEndMsg{id: e.ToolCallID, ok: !e.IsError, result: agentcore.ContentToText(e.Result.Content), details: e.Result.Details}
			case agentcore.SubAgentProgressEvent:
				ch <- subagentProgressMsg{id: e.ToolCallID, desc: e.Description, activity: e.Activity, tokens: e.Tokens}
			case agentcore.ContextUsageEvent:
				ch <- contextUsageMsg{tokens: e.Tokens, window: e.Window}
			case agentcore.TelemetryEvent:
				ch <- telemetryMsg{ev: e}
			case agentcore.CompactionStartEvent:
				ch <- compactionStartMsg{}
			case agentcore.CompactionEvent:
				ch <- compactionMsg{}
			}
		},
	}
}

// argsToMap coerces a tool call's untyped Args into a map[string]any. The event
// layer carries Args as an untyped any: the tool executor emits it as a
// json.RawMessage (the raw decoded JSON arguments), but a caller may also hand
// an already-decoded map. Both are supported here so the tool card can show the
// call's arguments; anything that is not a JSON object yields nil.
func argsToMap(args any) map[string]any {
	switch v := args.(type) {
	case map[string]any:
		return v
	case json.RawMessage:
		return unmarshalArgsMap(v)
	case []byte:
		return unmarshalArgsMap(v)
	case string:
		return unmarshalArgsMap([]byte(v))
	}
	return nil
}

// unmarshalArgsMap parses JSON object bytes into a map, returning nil for empty
// input or anything that is not a JSON object.
func unmarshalArgsMap(b []byte) map[string]any {
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// pump runs the agent loop to completion on the calling goroutine, converting
// every event to a tea.Msg on ch, and finally sends a runEndMsg carrying the
// run's result error. It is meant to be launched as a goroutine by startRun.
func pump(ctx context.Context, ch chan tea.Msg, agentCtx *agentcore.AgentContext, cfg runtime.RunConfig, onEvent func(agentcore.AgentEvent)) {
	// The pump is the run's only producer of its terminal event. A panic while
	// converting or observing an event (a plugin/hook callback, a handler bug)
	// must still hand runEndMsg to the UI — otherwise the model waits forever
	// in the running state for an event that will never come, and Ctrl+C has
	// nothing left to cancel. The loop-side panic path is covered in
	// runtime.agentLoop; this covers the driver side.
	// Own a cancelable child so the recovery path can stop the loop and let it
	// unwind before runEndMsg: the run-end handler persists agentCtx, and the
	// loop must not still be appending to it (the same guarantee the normal
	// path gets by sending runEndMsg only after DrainStream returns).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := runtime.StartRun(ctx, agentCtx, cfg)
	defer func() {
		if rec := recover(); rec != nil {
			cancel()
			// Drain to the loop's close so no writer outlives runEndMsg.
			for range stream.Events() {
			}
			ch <- runEndMsg{err: fmt.Errorf("run panic: %v\n%s", rec, debug.Stack())}
		}
	}()
	_, err := runtime.DrainStream(ctx, stream, newStreamHandler(ch, onEvent))
	ch <- runEndMsg{err: err}
}

// waitForEvent returns a tea.Cmd that blocks until the next bridge msg arrives.
// The Update loop re-issues it after handling each msg (except runEndMsg) to
// keep pulling events one at a time, so ordering is preserved and the tea
// goroutine never spins.
func waitForEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// startRun launches the run pump on a new goroutine and returns the channel it
// feeds together with the first waitForEvent Cmd. The caller (node #388's model)
// stores the channel and, on every subsequent event, issues waitForEvent(ch)
// again to pull the next msg. Returning the channel keeps the bridge
// self-contained: the model owns the handle and decides when to stop pulling
// (after runEndMsg).
func startRun(ctx context.Context, agentCtx *agentcore.AgentContext, cfg runtime.RunConfig, onEvent func(agentcore.AgentEvent)) (chan tea.Msg, tea.Cmd) {
	ch := newEventChan()
	return ch, startRunOn(ch, ctx, agentCtx, cfg, onEvent)
}

// startRunOn is startRun on a caller-owned channel. It exists for runs that
// must wire something else into the same event stream before the pump starts
// — the permission gate publishes review notes into ch so they render as
// transcript cards.
func startRunOn(ch chan tea.Msg, ctx context.Context, agentCtx *agentcore.AgentContext, cfg runtime.RunConfig, onEvent func(agentcore.AgentEvent)) tea.Cmd {
	go pump(ctx, ch, agentCtx, cfg, onEvent)
	return waitForEvent(ch)
}
