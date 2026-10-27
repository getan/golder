package agenttool

import (
	"bytes"
	"context"
	"image/color"
	"image/png"
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
		chrome := os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE")
		if chrome == "" {
			chrome = "/Applications/Chromium.app/Contents/MacOS/Chromium"
		}
		h, err := browserhost.Start(ctx, browserhost.Options{URL: "about:blank", Chrome: chrome,
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
			_, _ = w.Write([]byte("<!doctype html><html><head><title>controlled smoke</title><style>html,body{margin:0}body{min-height:1600px;background:#115599}</style></head><body>smoke page</body></html>"))
		}))
		defer page.Close()
		if _, _, err := c.Action(ctx, "navigate", map[string]any{"url": page.URL}, browserctl.Target{}, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		shot, err := c.Screenshot(ctx, false)
		if err != nil {
			t.Errorf("viewport screenshot: %v", err)
		} else {
			assertBrowserFixturePNG(t, shot, 1280, 800)
			full, err := c.Screenshot(ctx, true)
			if err != nil {
				t.Errorf("full-page screenshot: %v", err)
			} else {
				assertBrowserFixturePNG(t, full, 1280, 1600)
			}
			again, err := c.Screenshot(ctx, false)
			if err != nil {
				t.Errorf("viewport after full-page capture: %v", err)
			} else {
				assertBrowserFixturePNG(t, again, 1280, 800)
			}
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
	child := []string{"/usr/bin/env", "GOLDER_CHROMIUM_RUNTIME_CHILD=1", "GOLDER_CHROMIUM_TEST_ROOT=" + root, "GOLDER_TEST_CHROMIUM_EXECUTABLE=" + os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE"), exe, "-test.run=^TestBrowserRuntimeChromium$", "-test.count=1", "-test.v"}
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

func assertBrowserFixturePNG(t *testing.T, raw []byte, width, height int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Errorf("invalid screenshot PNG (%d bytes): %v", len(raw), err)
		return
	}
	if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Errorf("screenshot bounds=%v, want %dx%d", img.Bounds(), width, height)
		return
	}
	pixel := color.NRGBAModel.Convert(img.At(width/2, height/2)).(color.NRGBA)
	if pixel != (color.NRGBA{R: 0x11, G: 0x55, B: 0x99, A: 0xff}) {
		t.Errorf("fixture pixel=%v, want opaque #115599 (not a blank fallback)", pixel)
		return
	}
	t.Logf("verified PNG: %dx%d, %d bytes, fixture pixel #115599", width, height, len(raw))
}
