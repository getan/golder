package agenttool

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/tuiosctl"
)

// OpenAuthorizationPage starts an operator-only browser pane for one OAuth
// transaction. The caller supplies only the private loopback trampoline; the
// real authorization URL is never placed in pane argv or exposed to agents.
// There is no browser control socket, workspace browser record or shared login
// profile. The user controls the pane directly.
func OpenAuthorizationPage(ctx context.Context, cwd, startURL string) (func(), error) {
	u, err := url.Parse(startURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/mcp-start/") {
		return nil, errors.New("invalid private authorization start address")
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return nil, errors.New("invalid private authorization start address")
	}
	if !EmbeddedBrowserConfigured() {
		return nil, errors.New("the built-in authorization browser is unavailable")
	}
	root := golderhome.Dir()
	if root == "" {
		return nil, errors.New("private authorization storage is unavailable")
	}
	parent := filepath.Join(root, "extensions", "mcp-oauth-"+rand.Text()[:16])
	if insidePath(cwd, parent) {
		return nil, errors.New("authorization storage must be outside the project")
	}
	home := filepath.Join(parent, "home")
	for _, dir := range []string{root, filepath.Join(root, "extensions"), parent, home, filepath.Join(home, "profile")} {
		if err := privatepath.EnsureDir(dir); err != nil {
			return nil, errors.New("cannot create private authorization storage")
		}
	}
	remove := func() { _ = os.RemoveAll(parent) }
	capability, err := seatbelt.NewExtensionState(home)
	if err != nil {
		remove()
		return nil, err
	}
	argv, err := browserPaneArgv("", filepath.Join(home, "profile"), startURL)
	if err != nil {
		remove()
		return nil, errors.New("cannot prepare the built-in authorization browser")
	}
	report := filepath.Join(home, "startup.json")
	// Keep the explicitly empty control-socket argument: omitting the flag
	// would permit a KITTY_BROWSE_SOCKET default to expose the login pane.
	displayArgv := append(append(append([]string{}, argv[:2]...), "-golder-startup-report", report), argv[2:]...)
	name := authorizationPanePrefix + rand.Text()[:16]
	g := NewControlGuard(cwd)
	g.extensionState = capability
	// The person has just reviewed the OAuth host and explicitly chosen to open
	// this one networked authorization pane; this is not model-supplied consent.
	launchCtx := agentcore.WithNetworkGrant(agentcore.WithBrowserRuntimeGrant(ctx))
	c, err := tuiosctl.Dial(ctx, tuiosctl.FromEnv())
	if err != nil {
		remove()
		return nil, errors.New("no visible workspace authorization pane is available")
	}
	res, err := g.launch(launchCtx, c, tuiosctl.NewWindowParams{Name: name, Cwd: cwd, Command: displayArgv, Focus: true, KeepOpen: true}, true, true, true)
	_ = c.Close()
	if err != nil {
		remove()
		return nil, errors.New("cannot start the private authorization browser")
	}
	authorizationPanes.Store(res.WindowID, true)
	var once sync.Once
	closePane := func() {
		once.Do(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := tuiosctl.Dial(stopCtx, tuiosctl.FromEnv())
			if err != nil {
				return // leave a possibly live profile sealed rather than deleting it
			}
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
		return nil, errors.New("private authorization browser could not start")
	}
	return closePane, nil
}
