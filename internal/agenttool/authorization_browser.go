package agenttool

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	setupevent "github.com/getan/golder/extension/setup"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/tuiosctl"
	qrcode "github.com/skip2/go-qrcode"
)

const authorizationPanePrefix = "golder-authorization-"

var authorizationPanes sync.Map // window ID -> true; never a model capability

// OpenAuthorizationQR is operator-only. It shares the existing trusted browser
// launcher, but exposes NO browser control socket, workspace record or cookies.
// The desktop shows an offline, CSP-locked QR page; the phone opens the official
// authorization URL. This avoids both cross-origin desktop redirects and giving
// an agent a general browser page on which the person is logging in.
// Calling it represents the operator's explicit setup/browser-runtime consent.
func OpenAuthorizationQR(ctx context.Context, cwd string, event setupevent.Event) (func(), error) {
	if !EmbeddedBrowserConfigured() {
		return nil, errors.New("embedded browser is unavailable; use the same URL/QR in your browser or phone")
	}
	root := golderhome.Dir()
	if root == "" {
		return nil, errors.New("private authorization storage is unavailable")
	}
	parent := filepath.Join(root, "extensions", "mcp-auth-"+rand.Text()[:10])
	home := filepath.Join(parent, "home")
	for _, dir := range []string{root, filepath.Join(root, "extensions"), parent, home} {
		if err := privatepath.EnsureDir(dir); err != nil {
			return nil, err
		}
	}
	remove := func() { _ = os.RemoveAll(parent) }
	capability, err := seatbelt.NewExtensionState(home)
	if err != nil {
		remove()
		return nil, err
	}
	code, err := qrcode.Encode(event.VerificationURL, qrcode.Medium, 480)
	if err != nil {
		remove()
		return nil, errors.New("could not render authorization QR")
	}
	name := authorizationPanePrefix + rand.Text()[:10]
	page := "<!doctype html><html><head><meta charset=utf-8><meta http-equiv=Content-Security-Policy content=\"default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'\"><title>" + name + "</title><style>body{background:#101827;color:#e8edf5;font:22px sans-serif;text-align:center;padding:30px}img{width:360px;max-width:80vw;background:white;padding:16px;border-radius:12px}p{font-size:17px}</style></head><body><h2>请用飞书扫码授权</h2><img alt=\"请用手机飞书扫描此二维码\" src=\"data:image/png;base64," + base64.StdEncoding.EncodeToString(code) + "\"><p>" + html.EscapeString(event.Service) + " · 在手机确认权限后自动完成</p><p>授权页不向模型开放控制接口；取消请回到 Golder 按 Ctrl+C。</p></body></html>"
	pagePath := filepath.Join(home, "authorization.html")
	if err = os.WriteFile(pagePath, []byte(page), 0o600); err != nil {
		remove()
		return nil, err
	}
	profile := filepath.Join(home, "profile")
	if err = privatepath.EnsureDir(profile); err != nil {
		remove()
		return nil, err
	}
	pageURL := (&url.URL{Scheme: "file", Path: pagePath}).String()
	argv, err := browserPaneArgv("", profile, pageURL)
	if err != nil {
		remove()
		return nil, err
	}
	// No endpoint, even an undiscoverable one. This pane is display-only.
	var displayArgv []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-control-socket" {
			i++
			continue
		}
		displayArgv = append(displayArgv, argv[i])
	}
	report := filepath.Join(home, "startup.json")
	displayArgv = append(append(append([]string{}, displayArgv[:2]...), "-golder-startup-report", report), displayArgv[2:]...)
	g := NewControlGuard(cwd)
	g.extensionState = capability
	// Explicit operator setup, not model full-access. The exception is consumed
	// only by the trusted browser launcher; plugin execution stays sandboxed.
	launchCtx := agentcore.WithBrowserRuntimeGrant(ctx)
	c, err := tuiosctl.Dial(ctx, tuiosctl.FromEnv())
	if err != nil {
		remove()
		return nil, errors.New("no visible workspace browser pane is available")
	}
	res, err := g.launch(launchCtx, c, tuiosctl.NewWindowParams{Name: name, Cwd: cwd, Command: displayArgv, Focus: true, KeepOpen: true}, false, true, true)
	_ = c.Close()
	if err != nil {
		remove()
		return nil, err
	}
	authorizationPanes.Store(res.WindowID, true)
	var once sync.Once
	closePane := func() {
		once.Do(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := tuiosctl.Dial(stopCtx, tuiosctl.FromEnv())
			if err != nil {
				return
			} // leave any live private profile sealed
			defer client.Close()
			if err = client.CloseWindow(stopCtx, res.WindowID); err != nil {
				return
			}
			authorizationPanes.Delete(res.WindowID)
			g.release(res.WindowID)
			remove()
		})
	}
	spec := paneSpec{Embedded: true, StartupReport: report, Name: name}
	if err = waitForBrowserStartup(ctx, spec, browserSocketWait, nil); err != nil {
		closePane()
		return nil, fmt.Errorf("authorization viewer could not start: %w", err)
	}
	return closePane, nil
}

func sensitivePane(window tuiosctl.Window) bool {
	if _, ok := authorizationPanes.Load(window.WindowID); ok {
		return true
	}
	return strings.HasPrefix(window.DisplayName, authorizationPanePrefix) || strings.Contains(window.Title, authorizationPanePrefix)
}

func refuseSensitivePane(ctx context.Context, c *tuiosctl.Client, target string) error {
	list, err := c.ListWindows(ctx)
	if err != nil {
		return err
	}
	if target == "" {
		target = list.FocusedWindowID
	}
	for _, win := range list.Windows {
		if (win.WindowID == target || win.DisplayName == target) && sensitivePane(win) {
			return errors.New("authorization panes are operator-only; capture, screenshot and agent input are refused")
		}
	}
	return nil
}
