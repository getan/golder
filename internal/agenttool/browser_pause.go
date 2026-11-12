package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/kitty-browse/browserhost"
)

func validateBrowserPause(a browserArgs) error {
	if a.Action != "pause" && (a.Reason != "" || a.Justification != "") {
		return errors.New("reason and justification are supported only for pause")
	}
	if a.Reason != "" && a.Reason != "user_requested" && a.Reason != "assistance_required" {
		return errors.New("pause reason must be user_requested or assistance_required; detector/operator claims are host-only")
	}
	if utf8.RuneCountInString(a.Justification) > 240 || strings.ContainsAny(a.Justification, "\r\n") {
		return errors.New("pause justification must be one short line without secrets")
	}
	return nil
}

func readContinuationState(ctx context.Context, c *browserctl.Client, browser string) (browserhost.InteractionState, error) {
	raw, _, err := c.Action(ctx, "interaction.status", map[string]any{}, browserctl.Target{BrowserID: browser}, time.Second)
	var state browserhost.InteractionState
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	return state, err
}

func (t *BrowserTool) prepareContinuation(ctx context.Context, c *browserctl.Client, a browserArgs) (json.RawMessage, error) {
	state, err := readContinuationState(ctx, c, a.Browser)
	if err != nil {
		return nil, err
	}
	a.Socket = t.socketPath()
	if !state.Paused {
		a.Action, a.ContinuationID, a.InstanceEpoch, a.PauseGeneration = "status", "", "", 0
		return json.Marshal(a)
	}
	details := map[string]any{"browser": a.Browser, "pause_generation": state.PauseGeneration}
	if t.Control.Mode == nil || t.Control.Mode.Mode() != permissions.Ask {
		return nil, browserNotDispatched("browser_continuation_required", "use the displayed local Resume automation button once; no permission mode switch is needed", details)
	}
	t.registerContinuation(ctx)
	service := BrowserContinuations([]agentcore.AgentTool{t})
	service.Poll(ctx)
	for _, v := range service.Snapshot() {
		if v.BrowserID == a.Browser && v.Supported && v.Generation == state.PauseGeneration && v.InstanceEpoch == state.InstanceEpoch && !v.Busy && !v.OutcomeUnknown && !v.NeedsRefresh {
			if a.ContinuationID != "" && a.ContinuationID != v.ID || a.InstanceEpoch != "" && a.InstanceEpoch != v.InstanceEpoch || a.PauseGeneration != 0 && a.PauseGeneration != v.Generation {
				return nil, browserNotDispatched("browser_pause_changed", "the displayed recovery target changed; no decision was applied", nil)
			}
			a.ContinuationID, a.InstanceEpoch, a.PauseGeneration = v.ID, v.InstanceEpoch, v.Generation
			return json.Marshal(a)
		}
	}
	return nil, browserNotDispatched("browser_continuation_required", "this pause requires the exact local recovery control; inspect browser status for host support", details)
}

// Called only after OperatorConfirmedFor matched this exact prepared call. The
// public automation resume endpoint is never used, even in ask/full-access.
func (t *BrowserTool) resumeConfirmed(ctx context.Context, c *browserctl.Client, a browserArgs) (agentcore.AgentToolResult, error) {
	state, err := readContinuationState(ctx, c, a.Browser)
	if err != nil {
		return browserError(err), nil
	}
	if state.Paused {
		if a.ContinuationID == "" || a.PauseGeneration != state.PauseGeneration || a.InstanceEpoch != state.InstanceEpoch {
			return browserError(browserNotDispatched("browser_pause_changed", "the approved pause changed; no recovery was sent", nil)), nil
		}
		service := BrowserContinuations([]agentcore.AgentTool{t})
		if err := service.Continue(ctx, a.ContinuationID); err != nil {
			return browserError(err), nil
		}
		state, err = readContinuationState(ctx, c, a.Browser)
		if err != nil {
			return browserError(err), nil
		}
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{"ok": true, "result": state}))},
		Details: map[string]any{"action": "resume", "browser": a.Browser},
	}, nil
}
