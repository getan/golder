package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/permissions"
)

func TestAutoResumeStopsBeforeGenericApprovalAndNeverMutates(t *testing.T) {
	tool, f := regressionBrowser(t)
	tool.Control.Mode.Set(permissions.Auto)
	f.replies["interaction.status"] = `{"paused":true,"browserId":"browser-1","instanceEpoch":"epoch","pauseGeneration":9,"continuationVersion":1}`
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		t.Fatal("auto resume reached generic exact-call approval instead of the local recovery UI")
		return nil
	}
	result, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "resume", Name: "browser", Arguments: json.RawMessage(`{"action":"resume"}`)}, nil)
	if !result.IsError || !strings.Contains(textOf(result), "browser_continuation_required") || strings.Contains(textOf(result), "switch /permissions") {
		t.Fatalf("wrong recovery result: %s", textOf(result))
	}
	for _, action := range f.actions {
		if action["action"] != "interaction.status" {
			t.Fatalf("recovery preflight dispatched %q", action)
		}
	}
}

func TestResumeAlreadyRunningBecomesReadOnlyStatus(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["interaction.status"] = `{"paused":false}`
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"resume"}`))
	if err != nil {
		t.Fatal(err)
	}
	var args browserArgs
	_ = json.Unmarshal(prepared, &args)
	if args.Action != "status" || tool.RequiresOperatorConfirmationFor(prepared) {
		t.Fatal("already recovered browser requested another confirmation")
	}
}

func TestProfileLabelCannotManufactureBrowserOwnership(t *testing.T) {
	root := t.TempDir()
	tool := NewBrowserTool(root, true)
	instance := browserInstance{Socket: "unverified.sock", Info: browserctl.Info{BrowserID: "unverified", LoginProfile: browserLoginProfile(root, sharedBrowserProfileName)}}
	tool.discover = func(context.Context) browserDiscovery {
		return browserDiscovery{Instances: []browserInstance{instance}}
	}
	result, _ := tool.open(agentcore.WithNetworkGrant(context.Background()), browserArgs{Action: "open", Browser: instance.Info.BrowserID})
	if !result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), "browser_ownership_unverified") || len(tool.Control.browsers) != 0 {
		t.Fatal("public login-profile metadata became runtime ownership")
	}
}

func TestPauseReasonCannotImpersonateDetectorOrOperator(t *testing.T) {
	for _, args := range []browserArgs{{Action: "pause", Reason: "captcha"}, {Action: "pause", Reason: "operator_requested"}, {Action: "click", Reason: "assistance_required"}, {Action: "pause", Justification: "first\nsecond"}} {
		if validateBrowserPause(args) == nil {
			t.Fatalf("accepted forged pause claim: %+v", args)
		}
	}
	for _, args := range []browserArgs{{Action: "pause"}, {Action: "pause", Reason: "user_requested", Justification: "The person asked to pause this view."}} {
		if err := validateBrowserPause(args); err != nil {
			t.Fatal(err)
		}
	}
}
