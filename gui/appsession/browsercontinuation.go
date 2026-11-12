package appsession

import (
	"context"
	"errors"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browsercontinuation"
	"github.com/getan/golder/internal/permissions"
)

const browserContinuePrompt = "The person has resumed this conversation's browser automation using its local recovery control. Re-observe the current page and continue the previous request. Do not replay actions with unknown outcomes; the existing tool permissions still apply."

func (c *Controller) browserContinuation() *browsercontinuation.Coordinator {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	tools := c.opt.Tools
	c.mu.Unlock()
	return agenttool.BrowserContinuations(tools)
}

// PollBrowserContinuations is a trusted frontend subscription. Pane-origin
// resumes arrive here as checked host state, never as webpage text.
func (c *Controller) PollBrowserContinuations(ctx context.Context) []browsercontinuation.View {
	service := c.browserContinuation()
	if service == nil {
		return nil
	}
	service.Poll(ctx)
	ready := service.TakeReady()
	c.mu.Lock()
	for _, event := range ready {
		if event.BrowserID == c.browserWaitID && event.Generation == c.browserWaitGeneration && c.browserWaitRun != "" && c.browserWaitRun == c.runID {
			c.browserReady = true
		}
	}
	if c.browserWaitRun != "" && service.ReadyFor(c.browserWaitID, c.browserWaitGeneration) {
		c.browserReady = true
	}
	canContinue := c.browserReady && !c.running && !c.closed && !c.readOnly && c.perms.Mode() != permissions.ReadOnly && c.browserWaitRun == c.runID && len(c.steerQueue) == 0 && c.status.Error == ""
	id := c.header.ID
	waitRun := c.browserWaitRun
	c.mu.Unlock()
	if canContinue {
		prompt := queuedPrompt{content: agentcore.ContentList{agentcore.NewTextContent(browserContinuePrompt)}, text: browserContinuePrompt, display: browserContinuePrompt, browserWaitRun: waitRun}
		if err := c.submitPrepared(ctx, prompt, id, nil, false, true); err != nil {
			c.addNotice("Browser resumed; task did not restart: "+err.Error(), false)
		}
	}
	return service.Snapshot()
}

func (c *Controller) BrowserContinuations() []browsercontinuation.View {
	if service := c.browserContinuation(); service != nil {
		return service.Snapshot()
	}
	return nil
}

func (c *Controller) DisplayBrowserContinuation(requestID string) {
	if service := c.browserContinuation(); service != nil {
		service.Displayed(requestID)
	}
}

func (c *Controller) RefreshBrowserContinuation(ctx context.Context, sessionID, requestID string) error {
	c.mu.Lock()
	valid := !c.closed && !c.readOnly && c.header.ID == sessionID && c.perms.Mode() != permissions.ReadOnly
	c.mu.Unlock()
	if !valid {
		return errors.New("browser continuation belongs to another or read-only session")
	}
	service := c.browserContinuation()
	if service == nil || service.Session() != sessionID {
		return errors.New("browser continuation unavailable")
	}
	return service.Refresh(ctx, requestID)
}

// ResumeBrowser is for a real local UI event. Remote submit and Agent tools do
// not call it. An expired or replaced request cannot select a new target.
func (c *Controller) ResumeBrowser(ctx context.Context, sessionID, requestID string, keep bool) error {
	c.mu.Lock()
	valid := !c.closed && !c.readOnly && c.header.ID == sessionID && c.perms.Mode() != permissions.ReadOnly
	c.mu.Unlock()
	if !valid {
		return errors.New("browser continuation belongs to another or read-only session")
	}
	service := c.browserContinuation()
	if service == nil || service.Session() != sessionID {
		return errors.New("browser continuation unavailable")
	}
	if keep {
		return service.KeepPaused(requestID)
	}
	if err := service.Continue(ctx, requestID); err != nil {
		return err
	}
	c.PollBrowserContinuations(ctx)
	return nil
}
