package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browserbinding"
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
