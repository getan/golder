package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserbinding"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/kitty-browse/browserhost"
)

type approvedRecoveryHost struct {
	f     *fakeBrowser
	calls int
}

func (h *approvedRecoveryHost) State(context.Context) (browserhost.InteractionState, error) {
	var state browserhost.InteractionState
	err := json.Unmarshal([]byte(h.f.replies["interaction.status"]), &state)
	return state, err
}

func (h *approvedRecoveryHost) ContinueChecked(ctx context.Context, req browserhost.ContinueRequest) error {
	state, err := h.State(ctx)
	if err != nil || !state.Paused || state.BrowserID != req.BrowserID || state.InstanceEpoch != req.InstanceEpoch || state.PauseGeneration != req.PauseGeneration {
		return errors.New("browser_pause_changed")
	}
	h.calls++
	state.Paused, state.ResumedGeneration = false, state.PauseGeneration
	raw, _ := json.Marshal(state)
	h.f.replies["interaction.status"] = string(raw)
	return nil
}

func (h *approvedRecoveryHost) ContinuationOutcome(context.Context, browserhost.ContinueRequest) (string, error) {
	if h.calls > 0 {
		return "resumed", nil
	}
	return "not_consumed", nil
}

func TestAskCompatibilityResumePinsAndConsumesExactPause(t *testing.T) {
	for _, changed := range []bool{false, true} {
		tool, f := regressionBrowser(t)
		tools := []agentcore.AgentTool{tool}
		binding := ConfigureBrowserSession(tools, "recovery-conversation")
		binding.Select(browserbinding.Endpoint{Socket: "stub", BrowserID: "browser-1"})
		tool.Control.Mode.Set(permissions.Ask)
		tool.Control.rememberNativeBrowser("browser-1", "stub", sharedBrowserProfileName, browserProxyModeDirect)
		f.replies["interaction.status"] = `{"paused":true,"browserId":"browser-1","instanceEpoch":"browser-1","pauseGeneration":4,"continuationVersion":1}`
		h := &approvedRecoveryHost{f: f}
		service := BrowserContinuations(tools)
		t.Cleanup(service.Close)
		service.Register("fixture", "browser-1", "browser-1", "https://example.test", h)
		service.Poll(context.Background())
		prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"resume"}`))
		if err != nil {
			t.Fatal(err)
		}
		var args browserArgs
		_ = json.Unmarshal(prepared, &args)
		if args.PauseGeneration != 4 || args.InstanceEpoch != "browser-1" || args.ContinuationID == "" {
			t.Fatal("ask approval was not bound to its exact pending pause")
		}
		if changed {
			f.replies["interaction.status"] = `{"paused":true,"browserId":"browser-1","instanceEpoch":"browser-1","pauseGeneration":5,"continuationVersion":1}`
		}
		ctx := agentcore.WithNetworkGrant(context.Background())
		ctx = agentcore.WithOperatorConfirmation(ctx, "resume", "browser", prepared)
		result, err := tool.Execute(ctx, "resume", prepared, nil)
		if err != nil || result.IsError != changed {
			t.Fatalf("changed=%t: %v %s", changed, err, agentcore.ContentToText(result.Content))
		}
		if changed && h.calls != 0 || !changed && h.calls != 1 {
			t.Fatal("approval crossed a pause generation or submitted more than once")
		}
		if !changed {
			result, _ = tool.Execute(ctx, "resume", prepared, nil)
			if result.IsError || h.calls != 1 {
				t.Fatal("already recovered browser needed another private decision")
			}
		}
		for _, action := range f.actions {
			if action["action"] == "interaction.resume" {
				t.Fatal("compatibility path used unchecked public resume")
			}
		}
	}
}
