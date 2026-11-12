package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browsercontinuation/continuationtest"
	"github.com/getan/golder/internal/permissions"
)

func recoveryController(t *testing.T) (*Controller, *continuationtest.Host, string) {
	t.Helper()
	c, _, project := newTestController(t)
	tool := agenttool.NewBrowserTool(project, true)
	c.opt.Tools = []agentcore.AgentTool{tool}
	agenttool.ConfigureBrowserSession(c.opt.Tools, c.SessionID())
	h := continuationtest.New()
	h.Register(c.browserContinuation())
	return c, h, c.BrowserContinuations()[0].ID
}

func TestLocalRecoveryDoesNotChangePermissionsOrReplayActions(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.Auto, permissions.Ask} {
		c, h, id := recoveryController(t)
		c.perms.Set(mode)
		if err := c.ResumeBrowser(context.Background(), c.SessionID(), id, false); err != nil {
			t.Fatal(err)
		}
		if h.Calls() != 1 || c.perms.Mode() != mode || c.IsRunning() {
			t.Fatal("recovery changed mode or started a completed/non-waiting task")
		}
		if err := c.ResumeBrowser(context.Background(), c.SessionID(), id, false); err == nil || h.Calls() != 1 {
			t.Fatal("same recovery was consumed more than once")
		}
	}
}

func TestRecoverySessionReadOnlyCancelAndReservation(t *testing.T) {
	c, h, id := recoveryController(t)
	if err := c.ResumeBrowser(context.Background(), "other", id, false); err == nil {
		t.Fatal("recovery crossed session boundary")
	}
	c.perms.Set(permissions.ReadOnly)
	if err := c.ResumeBrowser(context.Background(), c.SessionID(), id, false); err == nil || h.Calls() != 0 {
		t.Fatal("read-only mode sent private recovery")
	}
	c.perms.Set(permissions.Auto)
	c.runID, c.browserWaitRun, c.browserReady = "old-run", "old-run", true
	c.Cancel()
	prompt := queuedPrompt{text: browserContinuePrompt, content: agentcore.ContentList{agentcore.NewTextContent(browserContinuePrompt)}, browserWaitRun: "old-run"}
	if err := c.submitPrepared(context.Background(), prompt, c.SessionID(), nil, false, true); err == nil || c.IsRunning() {
		t.Fatal("cancel lost the race against an unstarted continuation")
	}
	if err := c.ResumeBrowser(context.Background(), c.SessionID(), id, false); err != nil || c.IsRunning() {
		t.Fatal("browser-only recovery restarted a cancelled task")
	}
}
