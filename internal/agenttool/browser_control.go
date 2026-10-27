package agenttool

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"

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
	if a.Action == "open" {
		if a.NewInstance {
			a.Browser = ""
			return json.Marshal(a)
		}
		var candidates []browserInstance
		for _, instance := range t.discovery(ctx).Instances {
			if a.Browser != "" && instance.Info.BrowserID != a.Browser {
				continue
			}
			t.Control.mu.Lock()
			_, owned := t.Control.browsers[instance.Info.BrowserID]
			t.Control.mu.Unlock()
			if owned || a.HostControl || t.Control.full() {
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
	c, err := t.newClient()
	if err != nil {
		return nil, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	if a.Browser != "" && a.Browser != info.BrowserID {
		return nil, errors.New("selected browser does not match requested browser")
	}
	a.Browser = info.BrowserID
	if a.Action != "screenshot" {
		target, err := pinTab(info, t.tabChoice(a.Tab, info))
		if err != nil {
			return nil, err
		}
		a.Tab = target.TabID
		for _, tab := range info.Tabs {
			if tab.ID == target.TabID {
				a.PageURL = tab.URL
			}
		}
	}
	return json.Marshal(a)
}

func controlledBrowserSpec(g *ControlGuard, cwd, name, url string) (paneSpec, error) {
	if err := controlcap.BrowserURL(url); err != nil {
		return paneSpec{}, err
	}
	if g == nil {
		return paneSpec{}, errors.New("browser control policy is not configured")
	}
	profileName := name
	if !g.full() {
		profileName += "-" + g.id + "-" + rand.Text()[:8]
	}
	spec, err := browserPaneSpecFor(cwd, name, profileName, url)
	if err != nil {
		return paneSpec{}, err
	}
	spec.Profile = browserProfileDir(cwd, profileName)
	spec.Name = name
	if spec.Embedded {
		spec.StartupReport = filepath.Join(spec.Profile, ".startup-"+rand.Text()[:12]+".json")
		argv := append([]string{}, spec.Argv[:2]...)
		argv = append(argv, "-golder-startup-report", spec.StartupReport)
		spec.Argv = append(argv, spec.Argv[2:]...)
	}
	return spec, nil
}
