package mygoui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/selfupdate"
)

type guiRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// guiPackageURL only accepts the exact package names produced by the GUI
// workflow. The CLI release archive must never replace the desktop app.
func guiPackageURL(release guiRelease, current, goos, arch string) string {
	available, comparable := selfupdate.UpdateAvailable(current, release.Tag)
	if !comparable || !available || !strings.HasPrefix(release.Tag, "v") {
		return ""
	}
	version := strings.TrimPrefix(release.Tag, "v")
	for _, ch := range version {
		if !(ch >= '0' && ch <= '9' || ch == '.') {
			return ""
		}
	}
	var name string
	switch goos {
	case "darwin":
		if arch == "arm64" || arch == "amd64" {
			name = fmt.Sprintf("golder-gui-%s-%s.dmg", version, arch)
		}
	case "windows":
		if arch == "amd64" {
			name = fmt.Sprintf("golder-gui-setup-%s-amd64.exe", version)
		}
	case "linux":
		if arch == "arm64" || arch == "amd64" {
			name = fmt.Sprintf("golder-gui-%s-linux-%s.tar.gz", version, arch)
		}
	}
	if name == "" {
		return ""
	}
	for _, asset := range release.Assets {
		if asset.Name == name {
			return "https://github.com/" + selfupdate.Repo + "/releases/download/" + url.PathEscape(release.Tag) + "/" + url.PathEscape(name)
		}
	}
	return ""
}

func fetchGUIRelease(ctx context.Context, client *http.Client) (guiRelease, error) {
	var release guiRelease
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+selfupdate.Repo+"/releases/latest", nil)
	if err != nil {
		return release, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return release, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release, fmt.Errorf("release server returned %s", resp.Status)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release)
	return release, err
}

func (a *App) checkGUIUpdate(manual bool) {
	if a.updateBusy || a.ctrl == nil || a.win == nil {
		return
	}
	current, win := a.ctrl.Version(), a.win
	if !selfupdate.IsReleaseVersion(current) {
		if manual {
			a.banner, a.bannerErr = "Source builds cannot check for GUI updates.", false
		}
		return
	}
	a.updateBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		release, err := fetchGUIRelease(ctx, &http.Client{Timeout: 20 * time.Second})
		win.Update(func() {
			a.updateBusy = false
			if err != nil {
				a.updateError = err.Error()
				if manual {
					a.banner, a.bannerErr = "Update check failed: "+err.Error(), true
				}
				return
			}
			a.updateError = ""
			a.updateURL = guiPackageURL(release, current, runtime.GOOS, runtime.GOARCH)
			a.updateTag = ""
			if a.updateURL != "" {
				a.updateTag = release.Tag
			}
			if manual {
				if a.updateURL == "" {
					a.banner, a.bannerErr = "No newer GUI package is available for this platform.", false
				} else {
					a.banner, a.bannerErr = "GUI "+a.updateTag+" is available. Click Update again to download the installer.", false
				}
			}
		})
	}()
}

func (a *App) activateGUIUpdate() {
	if a.updateBusy {
		return
	}
	if a.updateURL == "" {
		a.checkGUIUpdate(true)
		return
	}
	if err := mygo.Shell.OpenExternal(a.updateURL); err != nil {
		a.banner, a.bannerErr = "Cannot open GUI download: "+err.Error(), true
		return
	}
	a.banner, a.bannerErr = "Download started. Install the package and restart Golder GUI to update.", false
}

func (a *App) updateButton(c *ui.Context) ui.Element {
	t := c.Theme()
	tip := "Check for GUI updates"
	if a.updateBusy {
		tip = "Checking for GUI updates…"
	}
	if a.updateTag != "" {
		tip = "Download GUI " + a.updateTag
	}
	button := ui.ButtonBase(c.Key("gui-update")).Size(30, 30).Radius(7).
		Background(t.Surface).TextColor(t.Text).Label("Update").Tooltip(tip)
	hoverBorder(c, button)
	if a.updateTag != "" {
		button.Background(ui.RGB(37, 99, 235)).TextColor(ui.RGB(255, 255, 255))
	} else if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() { ui.Icon(c, updateIcon).Size(16, 16) })
	return button
}
