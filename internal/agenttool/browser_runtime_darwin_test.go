package agenttool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/kitty-browse/browserhost"
)

func TestBrowserRuntimeGrantIsBrowserOnly(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS browser runtime policy")
	}
	t.Setenv("GOLDER_SANDBOX", "auto")
	g := NewControlGuard(t.TempDir())
	r := &controlRunner{}
	g.Runner = r
	ctx := agentcore.WithNetworkGrant(context.Background())
	if _, _, _, err := g.launchArgvFor(ctx, []string{"golder", "browse"}, g.Root, true, true); err == nil {
		t.Fatal("unapproved browser runtime was allowed")
	}
	ctx = agentcore.WithBrowserRuntimeGrant(ctx)
	argv, cleanup, isolated, err := g.launchArgvFor(ctx, []string{"golder", "browse"}, g.Root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if isolated || len(r.argv) != 0 || !strings.Contains(strings.Join(argv, " "), "CFFIXED_USER_HOME=") {
		t.Fatalf("browser envelope=%v isolated=%t", argv, isolated)
	}
	_, cleanBash, isolated, err := g.launchArgv(ctx, []string{"bash", "-c", "pwd"}, g.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanBash()
	if !isolated || len(r.argv) == 0 {
		t.Fatal("browser grant escaped a normal bash helper")
	}
	if _, _, _, err := g.launchArgvFor(agentcore.WithSandboxRequest(ctx), []string{"golder", "browse"}, g.Root, true, true); err == nil {
		t.Fatal("browser exception defeated an explicit containment request")
	}
	t.Setenv("GOLDER_SANDBOX", "enforce")
	if _, _, _, err := g.launchArgvFor(ctx, []string{"golder", "browse"}, g.Root, true, true); err == nil {
		t.Fatal("browser exception defeated enforce")
	}
}

// This subprocess follows the real launch envelope (including the clean HOME),
// launches installed Chromium, then proves CDP, DOM, screenshot and shutdown.
// Opt-in because ordinary CI must not require a browser or GUI services.
func TestBrowserRuntimeChromium(t *testing.T) {
	if os.Getenv("GOLDER_CHROMIUM_RUNTIME_CHILD") == "1" {
		root := os.Getenv("GOLDER_CHROMIUM_TEST_ROOT")
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		h, err := browserhost.Start(ctx, browserhost.Options{URL: "about:blank", Chrome: "/Applications/Chromium.app/Contents/MacOS/Chromium",
			ProfileDir: filepath.Join(root, "profile"), Socket: filepath.Join(root, "c.sock"), RequireSandbox: true, LoadTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		c, err := browserctl.Dial(filepath.Join(root, "c.sock"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseIdleConnections()
		info, err := c.Info(ctx)
		if err != nil || info.Backend != "native-cdp" {
			t.Fatalf("CDP info=%+v err=%v", info, err)
		}
		if _, _, err := c.Action(ctx, "get", map[string]any{"kind": "html"}, browserctl.Target{}, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<!doctype html><html><head><title>controlled smoke</title></head><body>smoke page</body></html>"))
		}))
		defer page.Close()
		if _, _, err := c.Action(ctx, "navigate", map[string]any{"url": page.URL}, browserctl.Target{}, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		png, err := c.Screenshot(ctx, false)
		if err != nil || len(png) < 8 {
			t.Errorf("screenshot bytes=%d err=%v", len(png), err)
		}
		if err := c.Quit(ctx); err != nil {
			t.Fatal(err)
		}
		t.Log("CDP, DOM, navigation and normal quit completed")
		return
	}
	if os.Getenv("GOLDER_TEST_CHROMIUM_RUNTIME") != "1" {
		t.Skip("opt-in installed Chromium/CDP runtime test")
	}
	t.Setenv("GOLDER_SANDBOX", "auto")
	root, err := os.MkdirTemp("", "gbr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	g := NewControlGuard(root)
	ctx := agentcore.WithBrowserRuntimeGrant(agentcore.WithNetworkGrant(context.Background()))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := []string{"/usr/bin/env", "GOLDER_CHROMIUM_RUNTIME_CHILD=1", "GOLDER_CHROMIUM_TEST_ROOT=" + root, exe, "-test.run=^TestBrowserRuntimeChromium$", "-test.count=1", "-test.v"}
	argv, cleanup, isolated, err := g.launchArgvFor(ctx, child, root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if isolated {
		t.Fatal("approved Chromium was put back under outer Seatbelt")
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("controlled Chromium helper: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}
