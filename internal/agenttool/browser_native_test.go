package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestBrowserOpenUsesConfiguredNativeHostOutsideTuios(t *testing.T) {
	withBrowserSocket(t, "")
	t.Setenv("TUIOS_ENV", "")
	t.Setenv("TUIOS_SOCKET", "")
	t.Setenv("TUIOS_SESSION", "")
	t.Setenv("TUIOS_PANE_ID", "")
	var request NativeBrowserRequest
	ConfigureNativeBrowserLauncher(func(_ context.Context, req NativeBrowserRequest) error {
		request = req
		serveDiscoveredBrowser(t, req.Socket, "native-browser")
		return nil
	})
	t.Cleanup(func() { ConfigureNativeBrowserLauncher(nil) })

	tool := NewBrowserTool(shortRoot(t), true)
	trustFakeControl(tool.Control)
	result := runBrowserCall(t, tool, `{"action":"open","url":"https://example.com"}`)
	if result.IsError {
		t.Fatal(agentcore.ContentToText(result.Content))
	}
	if request.Socket == "" || request.ProfileDir == "" || request.LoginProfile == "" {
		t.Fatalf("native request is incomplete: %+v", request)
	}
	tool.Control.Mode.Set(permissions.Auto)
	if err := tool.Control.checkBrowser(context.Background(), "native-browser", request.Socket, false); err != nil {
		t.Fatalf("the newly launched browser was not owned: %v", err)
	}
	controlCtx := agentcore.WithNetworkGrant(context.Background())
	navigation, err := tool.Execute(controlCtx, "navigate", json.RawMessage(`{"action":"navigate","url":"https://example.com/next"}`), nil)
	if err != nil || navigation.IsError {
		t.Fatalf("native browser rejected a subsequent navigation: %v %s", err, agentcore.ContentToText(navigation.Content))
	}
	if err := tool.Control.checkBrowser(context.Background(), "native-browser", request.Socket+"-other", false); err == nil {
		t.Fatal("another socket inherited native browser ownership")
	}
	if err := NewControlGuard(tool.cwd).checkBrowser(context.Background(), "native-browser", request.Socket, false); err == nil {
		t.Fatal("native browser ownership leaked to another agent")
	}
	if err := tool.Control.checkBrowser(agentcore.WithSandboxRequest(context.Background()), "native-browser", request.Socket, false); err == nil {
		t.Fatal("native browser satisfied an explicit outer sandbox request")
	}
	if !strings.Contains(agentcore.ContentToText(result.Content), `"surface":"native"`) {
		t.Fatalf("result = %s", agentcore.ContentToText(result.Content))
	}
	if !strings.Contains(tool.Description(), "Bitmap surface") {
		t.Fatalf("description does not explain the GUI surface: %s", tool.Description())
	}
}
