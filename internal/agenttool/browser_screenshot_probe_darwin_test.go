package agenttool

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/getan/golder/internal/agentcore"
)

// A synthetic-page diagnostic comparison, opt-in and never installed over a
// running client. Each arm uses the same approved clean-home launch envelope.
func TestBrowserScreenshotLayerProbe(t *testing.T) {
	if mode := os.Getenv("GOLDER_SCREENSHOT_PROBE_CHILD"); mode != "" {
		root := os.Getenv("GOLDER_SCREENSHOT_PROBE_ROOT")
		chrome := os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE")
		if chrome == "" {
			chrome = "/Applications/Chromium.app/Contents/MacOS/Chromium"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		switch mode {
		case "cli":
			path := filepath.Join(root, "screenshot.png")
			cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-first-run", "--no-default-browser-check", "--password-store=basic", "--use-mock-keychain", "--disable-features=MacAppCodeSignClone", "--user-data-dir="+filepath.Join(root, "profile"), "--screenshot="+path, "--timeout=5000", "about:blank")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.WaitDelay = 2 * time.Second
			var output screenshotProbeOutput
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Run(); err != nil {
				t.Fatalf("Chromium CLI: %v\n%s", err, output.String())
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("CLI image: %v\n%s", err, output.String())
			}
			assertProbePNG(t, raw)
		case "cdp", "cdp-stock", "cdp-no-clone", "cdp-library", "cdp-surface", "cdp-focus", "cdp-surface-focus":
			opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
			opts = append(opts, chromedp.ExecPath(chrome), chromedp.UserDataDir(filepath.Join(root, "profile")), chromedp.Flag("no-sandbox", false))
			if mode == "cdp-stock" || mode == "cdp-no-clone" {
				// Hold IPC/temp state constant in the stock/no-clone A/B test.
				// Code-sign clones use a separate OS directory, not TMPDIR;
				// graceful browser shutdown must let Chromium clean its own clone.
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				tmp, err := os.MkdirTemp(filepath.Dir(exe), "ct-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(tmp)
				if err := os.Setenv("TMPDIR", tmp+string(os.PathSeparator)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cdp-no-clone" || mode == "cdp-library" {
				opts = append(opts, chromedp.Flag("disable-features", "Translate,BlinkGenPropertyTrees,MacAppCodeSignClone"))
			} else if mode != "cdp-stock" {
				opts = append(opts, chromedp.Flag("disable-features", "MacAppCodeSignClone"))
			}
			if strings.Contains(mode, "surface") {
				opts = append(opts, chromedp.Flag("enable-features", "NetworkService,NetworkServiceInProcess,CDPScreenshotNewSurface"))
			}
			alloc, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
			defer closeAlloc()
			browser, closeBrowser := chromedp.NewContext(alloc)
			defer func() {
				shutdown, stop := context.WithTimeout(browser, 5*time.Second)
				_ = chromedp.Cancel(shutdown)
				stop()
				closeBrowser()
			}()
			if err := chromedp.Do(browser, chromedp.Navigate("about:blank")); err != nil {
				t.Fatal(err)
			}
			shot, stopShot := context.WithTimeout(browser, 10*time.Second)
			defer stopShot()
			if strings.Contains(mode, "focus") {
				if _, err := chromedp.Call(shot, emulation.SetFocusEmulationEnabled, emulation.SetFocusEmulationEnabledParams{Enabled: true}); err != nil {
					t.Fatalf("focus emulation: %v", err)
				}
				if _, err := chromedp.Call(shot, page.BringToFront, cdp.Empty{}); err != nil {
					t.Fatalf("bring to front: %v", err)
				}
				if _, err := chromedp.Call(shot, cdpruntime.Evaluate, cdpruntime.EvaluateParams{Expression: "document.documentElement.offsetHeight", ReturnByValue: probeBool(true)}); err != nil {
					t.Fatalf("layout sync: %v", err)
				}
			}
			res, err := chromedp.Call(shot, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng, FromSurface: probeBool(true), CaptureBeyondViewport: probeBool(false)})
			if err != nil {
				t.Fatalf("bare CDP screenshot: %v", err)
			}
			assertProbePNG(t, res.Data)
		default:
			t.Fatalf("unknown probe mode %q", mode)
		}
		return
	}
	if os.Getenv("GOLDER_TEST_SCREENSHOT_LAYERS") != "1" {
		t.Skip("opt-in installed Chromium layer comparison")
	}
	modes := []string{"cli", "cdp"}
	if raw := os.Getenv("GOLDER_TEST_SCREENSHOT_MODES"); raw != "" {
		modes = strings.Split(raw, ",")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			root, err := os.MkdirTemp("", "gbp-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			t.Setenv("GOLDER_SANDBOX", "auto")
			g := NewControlGuard(root)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := []string{"/usr/bin/env", "GOLDER_SCREENSHOT_PROBE_CHILD=" + mode, "GOLDER_SCREENSHOT_PROBE_ROOT=" + root, "GOLDER_TEST_CHROMIUM_EXECUTABLE=" + os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE"), exe, "-test.run=^TestBrowserScreenshotLayerProbe$", "-test.count=1", "-test.v"}
			grant := agentcore.WithBrowserRuntimeGrant(agentcore.WithNetworkGrant(context.Background()))
			argv, cleanup, isolated, err := g.launchArgvFor(grant, child, root, true, true)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if isolated {
				t.Fatal("probe inherited outer Seatbelt")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Dir = root
			cmd.WaitDelay = 2 * time.Second
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s probe: %v\n%s", mode, err, out)
			}
			t.Logf("%s", out)
		})
	}
}

func probeBool(b bool) *bool { return &b }

func assertProbePNG(t *testing.T, raw []byte) {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("invalid PNG (%d bytes): %v", len(raw), err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		t.Fatalf("empty PNG: %+v", cfg)
	}
	t.Logf("decoded PNG: %dx%d, %d bytes", cfg.Width, cfg.Height, len(raw))
}

type screenshotProbeOutput struct {
	mu  sync.Mutex
	buf []byte
}

func (b *screenshotProbeOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := (16 << 10) - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
func (b *screenshotProbeOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
