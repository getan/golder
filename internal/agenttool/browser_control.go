package agenttool

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/controlcap"
)

// PrepareCall pins the selected browser/tab before review; Execute refuses an
// identity mismatch rather than sending the approved action to a replacement.
func (t *BrowserTool) PrepareCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if t.Control == nil {
		return nil, errors.New("browser control policy is not configured")
	}
	var a browserArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if err := controlcap.BrowserURL(a.URL); err != nil {
		return nil, err
	}
	if (a.Action == "tab_close" || a.Action == "tab_select") && strings.TrimSpace(a.Tab) == "" {
		return nil, errors.New(a.Action + " needs an explicit tab id from list")
	}
	if a.Profile != "" && (a.Action != "open" || !browserProfileName.MatchString(a.Profile)) {
		return nil, errors.New("profile is a validated name supported only by open")
	}
	if err := validateBrowserPause(a); err != nil {
		return nil, err
	}
	if a.Action == "open" {
		if a.NewInstance {
			a.Browser = ""
			return json.Marshal(a)
		}
		var candidates []browserInstance
		effectiveProfile := effectiveBrowserProfileName(a.Profile)
		for _, instance := range t.discovery(ctx).Instances {
			if a.Browser != "" && instance.Info.BrowserID != a.Browser {
				continue
			}
			t.Control.mu.Lock()
			ownedBrowser, owned := t.Control.browsers[instance.Info.BrowserID]
			t.Control.mu.Unlock()
			profileMatch := instance.Info.LoginProfile == browserLoginProfile(t.cwd, effectiveProfile)
			if a.Profile != "" && !profileMatch && (!owned || ownedBrowser.profile != a.Profile) {
				continue
			}
			ownedMatch := owned && ownedBrowser.profile == effectiveProfile
			if ownedMatch || profileMatch || a.HostControl || t.Control.full() {
				candidates = append(candidates, instance)
			}
		}
		if len(candidates) > 1 {
			var selected []browserInstance
			for _, instance := range candidates {
				if instance.Selected {
					selected = append(selected, instance)
				}
			}
			candidates = selected
			if len(candidates) != 1 {
				return nil, errors.New("open needs an exact browser id when several instances are available")
			}
		}
		if len(candidates) == 1 {
			a.Browser = candidates[0].Info.BrowserID
		} else {
			if a.Browser != "" {
				return nil, errors.New("requested browser is unavailable")
			}
			a.NewInstance = true
		}
		return json.Marshal(a)
	}
	if a.Action == "list" || a.Action == "attach" {
		return raw, nil
	}
	selectedSocket := t.socketPath()
	c, err := t.clientForSocket(selectedSocket)
	if err != nil {
		return nil, err
	}
	defer c.CloseIdleConnections()
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	if a.Browser != "" && a.Browser != info.BrowserID {
		return nil, errors.New("selected browser does not match requested browser")
	}
	a.Browser = info.BrowserID
	if a.Action == "resume" {
		return t.prepareContinuation(ctx, c, a)
	}
	if t.Control.binding() != nil {
		if selectedSocket == "" || t.socketPath() != selectedSocket {
			return nil, errors.New("browser binding changed during preparation")
		}
		a.Socket = selectedSocket
	}
	if a.Action == "quit" {
		if info.BrowserID == "" || selectedSocket == "" || t.socketPath() != selectedSocket {
			return nil, errors.New("cannot pin the selected browser and socket for quit")
		}
		a.Socket = selectedSocket // host-supplied target fact, never a model capability
	}
	if a.Action != "status" && a.Action != "pause" && a.Action != "resume" && a.Action != "quit" {
		target, err := t.resolveTarget(info, a.Tab)
		if err != nil {
			return nil, err
		}
		if a.TargetID != "" && a.TargetID != target.TargetID {
			return nil, errors.New("selected tab target does not match the requested target")
		}
		a.Tab = target.TabID
		a.TargetID = target.TargetID
		for _, tab := range info.Tabs {
			if tab.ID == target.TabID {
				if a.PageURL != "" && a.PageURL != tab.URL {
					return nil, errors.New("selected tab URL does not match the requested page")
				}
				a.PageURL = tab.URL
			}
		}
	}
	return json.Marshal(a)
}

func controlledBrowserSpec(g *ControlGuard, cwd, name, url string) (paneSpec, error) {
	return controlledBrowserSpecWithProfile(g, cwd, name, url, "")
}

func controlledBrowserSpecWithProfile(g *ControlGuard, cwd, name, url, persistent string) (paneSpec, error) {
	if err := controlcap.BrowserURL(url); err != nil {
		return paneSpec{}, err
	}
	if g == nil {
		return paneSpec{}, errors.New("browser control policy is not configured")
	}
	persistent = effectiveBrowserProfileName(persistent)
	if !g.profileAllowed(persistent) {
		return paneSpec{}, errors.New("saved browser logins have not been authorized for this agent session")
	}
	profileName := name
	if !g.full() || persistent != sharedBrowserProfileName {
		profileName += "-" + g.id + "-" + rand.Text()[:8]
	}
	spec, err := browserPaneSpecWithProfile(cwd, name, profileName, url, persistent, g.conversation())
	if err != nil {
		return paneSpec{}, err
	}
	spec.ProxyMode = browserProxyModeDirect
	if g.BrowserProxy != nil {
		proxy, err := g.BrowserProxy()
		if err != nil {
			return paneSpec{}, err
		}
		if proxy != "" {
			spec.ProxyMode = browserProxyModeProxy
			spec.ProxyServer = proxy
			last := len(spec.Argv) - 1 // the single positional URL is always last
			argv := append([]string{}, spec.Argv[:last]...)
			argv = append(argv, "-proxy-server", proxy, spec.Argv[last])
			spec.Argv = argv
		}
	}
	spec.Name = name
	if spec.Embedded {
		spec.StartupReport = filepath.Join(browserStateDir(cwd), ".startup-"+rand.Text()[:12]+".json")
		argv := append([]string{}, spec.Argv[:2]...)
		argv = append(argv, "-golder-startup-report", spec.StartupReport)
		spec.Argv = append(argv, spec.Argv[2:]...)
	}
	return sessionSpec(g, cwd, spec)
}
