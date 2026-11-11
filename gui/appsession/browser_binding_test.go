package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browserbinding"
	"github.com/getan/golder/internal/session"
)

func TestControllerAndAgentShareBrowserBinding(t *testing.T) {
	_, store, project := newTestController(t)
	tool := agenttool.NewBrowserTool(project, true)
	tools := []agentcore.AgentTool{tool}
	c := New(Options{Cwd: project, Store: store, Tools: tools})
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if c.BrowserBinding() != agenttool.ConfigureBrowserSession(tools, c.SessionID()) {
		t.Fatal("controller and Agent have independent browser bindings")
	}
}

func TestControllerRejectsForeignBindingAndStaleBrowserRow(t *testing.T) {
	c, _, _ := newTestController(t)
	foreign := browserbinding.New("other-conversation")
	preview := BrowserPreview{Socket: "/tmp/other-conversation.sock", Browser: "other"}
	if err := c.SelectBrowser(context.Background(), foreign, preview); err == nil {
		t.Fatal("foreign binding accepted")
	}
	if err := c.SelectBrowser(context.Background(), c.BrowserBinding(), preview); err == nil {
		t.Fatal("stale browser row accepted without current session discovery")
	}
	if c.BrowserBinding().Snapshot().Endpoint.Socket != "" {
		t.Fatal("invalid browser row changed the binding")
	}
	if state, browsers := c.BrowserSnapshot(context.Background(), foreign); state.SessionID != "" || len(browsers) != 0 {
		t.Fatal("foreign subscription received discovery results")
	}
}

func TestControllerBrowserBindingTracksSameProjectSessionABA(t *testing.T) {
	_, store, project := newTestController(t)
	tool := agenttool.NewBrowserTool(project, true)
	tools := []agentcore.AgentTool{tool}
	c := New(Options{Cwd: project, Store: store, Tools: tools})
	ctx := context.Background()
	if err := c.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	aID, aBinding := c.SessionID(), c.BrowserBinding()
	if err := store.Save(c.header, nil); err != nil {
		t.Fatal(err)
	}
	aBinding.Select(browserbinding.Endpoint{Socket: "/tmp/old-browser.sock", BrowserID: "old"})
	if err := c.NewSession(ctx); err != nil {
		t.Fatal(err)
	}
	bBinding := c.BrowserBinding()
	if bBinding == aBinding || bBinding != agenttool.ConfigureBrowserSession(tools, c.SessionID()) || bBinding.Snapshot().Endpoint.Socket != "" {
		t.Fatal("new session did not atomically share a fresh binding")
	}
	if c.currentBrowserBinding(aBinding) {
		t.Fatal("old GUI callback remained current after A -> B")
	}
	if err := c.Resume(ctx, aID); err != nil {
		t.Fatal(err)
	}
	if c.BrowserBinding() == aBinding || c.BrowserBinding() == bBinding || c.BrowserBinding() != agenttool.ConfigureBrowserSession(tools, aID) {
		t.Fatal("resume did not synchronize controller and tool binding")
	}
	if c.currentBrowserBinding(aBinding) || c.currentBrowserBinding(bBinding) {
		t.Fatal("stale A/B callbacks survived A -> B -> A")
	}
	if err := c.SelectBrowser(ctx, aBinding, BrowserPreview{Socket: "/tmp/old-browser.sock", Browser: "old"}); err == nil {
		t.Fatal("old observation became control authority after resume")
	}
}

func TestControllerReadOnlyResumeStillSynchronizesBrowserBinding(t *testing.T) {
	_, store, project := newTestController(t)
	tool := agenttool.NewBrowserTool(project, true)
	tools := []agentcore.AgentTool{tool}
	c := New(Options{Cwd: project, Store: store, Tools: tools})
	ctx := context.Background()
	if err := c.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	old := c.BrowserBinding()
	header := session.SessionHeader{ID: "locked-browser-session", Cwd: project}
	if err := store.Save(header, nil); err != nil {
		t.Fatal(err)
	}
	writer, err := store.AcquireWriter(header.ID, "TUI")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	if err := c.Resume(ctx, header.ID); err != nil {
		t.Fatal(err)
	}
	if !c.Snapshot().ReadOnly || c.BrowserBinding() == old || c.BrowserBinding().Snapshot().SessionID != header.ID ||
		c.BrowserBinding() != agenttool.ConfigureBrowserSession(tools, header.ID) {
		t.Fatal("read-only resume retained a stale browser binding")
	}
}
