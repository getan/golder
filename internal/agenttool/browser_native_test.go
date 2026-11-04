package agenttool

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
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
	if !strings.Contains(agentcore.ContentToText(result.Content), `"surface":"native"`) {
		t.Fatalf("result = %s", agentcore.ContentToText(result.Content))
	}
	if !strings.Contains(tool.Description(), "Bitmap surface") {
		t.Fatalf("description does not explain the GUI surface: %s", tool.Description())
	}
}
