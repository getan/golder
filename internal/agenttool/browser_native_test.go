package agenttool

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestNativeBrowserRequiresReviewedRuntimeOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS browser runtime policy")
	}
	withBrowserSocket(t, "")
	t.Setenv("TUIOS_ENV", "")
	t.Setenv("TUIOS_SOCKET", "")
	t.Setenv("TUIOS_SESSION", "")
	t.Setenv("TUIOS_PANE_ID", "")
	launched := false
	ConfigureNativeBrowserLauncher(func(_ context.Context, req NativeBrowserRequest) error {
		launched = true
		serveDiscoveredBrowser(t, req.Socket, "native-reviewed")
		return nil
	})
	t.Cleanup(func() { ConfigureNativeBrowserLauncher(nil) })
	tool := NewBrowserTool(shortRoot(t), true)
	raw := json.RawMessage(`{"action":"open","url":"https://example.com"}`)
	ctx := agentcore.WithNetworkGrant(context.Background())
	result, err := tool.Execute(ctx, "test", raw, nil)
	if err != nil || !result.IsError || launched || !strings.Contains(agentcore.ContentToText(result.Content), "reviewed browser runtime authorization") {
		t.Fatalf("unreviewed browser open was not refused: err=%v result=%s", err, agentcore.ContentToText(result.Content))
	}
	result, err = tool.Execute(agentcore.WithBrowserRuntimeGrant(ctx), "test", raw, nil)
	if err != nil || result.IsError || !launched {
		t.Fatalf("reviewed browser open failed: err=%v result=%s", err, agentcore.ContentToText(result.Content))
	}
}

func TestNativeBrowserStartupHasBoundedContext(t *testing.T) {
	withBrowserSocket(t, "")
	t.Setenv("TUIOS_ENV", "")
	t.Setenv("TUIOS_SOCKET", "")
	t.Setenv("TUIOS_SESSION", "")
	t.Setenv("TUIOS_PANE_ID", "")
	started := false
	ConfigureNativeBrowserLauncher(func(ctx context.Context, _ NativeBrowserRequest) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > browserNativeStartupWait {
			t.Fatal("native startup was not bounded")
		}
		started = true
		<-ctx.Done()
		return ctx.Err()
	})
	t.Cleanup(func() { ConfigureNativeBrowserLauncher(nil) })
	tool := NewBrowserTool(shortRoot(t), true)
	trustFakeControl(tool.Control)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := tool.Execute(ctx, "test", json.RawMessage(`{"action":"open","url":"https://example.com"}`), nil)
	if err != nil || !started || !result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), "deadline exceeded") {
		t.Fatalf("startup timeout result=%s err=%v started=%t", agentcore.ContentToText(result.Content), err, started)
	}
}
