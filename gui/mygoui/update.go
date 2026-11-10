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
	Tag    string            `json:"tag_name"`
	Assets []guiReleaseAsset `json:"assets"`
}

type guiReleaseAsset struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// guiPackageURL only accepts the exact package names produced by the GUI
// workflow. The CLI release archive must never replace the desktop app.
func guiPackageURL(release guiRelease, current, goos, arch string) string {
	available, comparable := selfupdate.UpdateAvailable(current, release.Tag)
	if !comparable || !available {
		return ""
	}
	name := guiAssetName(release.Tag, goos, arch)
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

func guiAssetName(tag, goos, arch string) string {
	version, ok := strings.CutPrefix(tag, "v")
	if !ok || len(version) > 64 {
		return ""
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return ""
			}
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
	return name
}

// guiReleaseDownload refuses to offer an installer when GitHub has not yet
// published its size and digest; it must not silently skip verification.
func guiReleaseDownload(release guiRelease, current, goos, arch string) (guiDownload, bool, error) {
	packageURL := guiPackageURL(release, current, goos, arch)
	if packageURL == "" {
		return guiDownload{}, false, nil
	}
	name := guiAssetName(release.Tag, goos, arch)
	for _, asset := range release.Assets {
		if asset.Name == name {
			digest, ok := strings.CutPrefix(asset.Digest, "sha256:")
			if asset.State != "uploaded" || !ok {
				return guiDownload{}, false, fmt.Errorf("GitHub has not published a SHA-256 for %s", name)
			}
			pkg := guiDownload{Tag: release.Tag, Name: name, URL: packageURL, SHA256: digest, Size: asset.Size, GOOS: goos, GOARCH: arch}
			if err := pkg.validate(); err != nil {
				return guiDownload{}, false, fmt.Errorf("invalid release metadata: %w", err)
			}
			return pkg, true, nil
		}
	}
	return guiDownload{}, false, nil
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
	ctx, cancel, seq := a.beginGUIUpdateOperation("checking", 20*time.Second)
	go func() {
		defer cancel()
		client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.User != nil {
				return fmt.Errorf("unsafe release API redirect")
			}
			return nil
		}}
		release, err := fetchGUIRelease(ctx, client)
		var pkg guiDownload
		var available bool
		if err == nil {
			pkg, available, err = guiReleaseDownload(release, current, runtime.GOOS, runtime.GOARCH)
		}
		win.Update(func() {
			if !a.finishGUIUpdateOperation(seq) {
				return
			}
			startDownload := manual || a.updateAfterCheck
			a.updateAfterCheck = false
			if err != nil {
				a.updateError = err.Error()
				if startDownload {
					a.banner, a.bannerErr = "Update check failed: "+err.Error(), true
				}
				return
			}
			a.updateError = ""
			a.updatePackage = pkg
			a.updateTag = ""
			if available {
				a.updateTag = release.Tag
			}
			if startDownload {
				if !available {
					a.banner, a.bannerErr = "No newer GUI package is available for this platform.", false
				} else {
					a.startGUIDownload()
				}
			}
		})
	}()
}

func (a *App) activateGUIUpdate() {
	if a.updateBusy {
		if a.updatePhase == "checking" {
			a.updateAfterCheck = true
			a.banner, a.bannerErr = "Checking for GUI updates…", false
		}
		return
	}
	if a.updatePath != "" {
		a.updateDialogOpen = true
		return
	}
	if a.updatePackage.URL == "" {
		a.checkGUIUpdate(true)
		return
	}
	a.startGUIDownload()
}

func (a *App) beginGUIUpdateOperation(phase string, timeout time.Duration) (context.Context, context.CancelFunc, uint64) {
	a.updateSeq++
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	a.updateCancel, a.updateBusy, a.updatePhase = cancel, true, phase
	return ctx, cancel, a.updateSeq
}

func (a *App) finishGUIUpdateOperation(seq uint64) bool {
	if seq != a.updateSeq {
		return false
	}
	a.updateCancel, a.updateBusy, a.updatePhase = nil, false, ""
	return true
}

func (a *App) cancelGUIUpdate() {
	if a.updateCancel != nil {
		a.updateCancel()
		a.updateCancel = nil
		a.updateSeq++
		a.updateBusy, a.updatePhase, a.updateAfterCheck = false, "", false
	}
}

func (a *App) startGUIDownload() {
	if a.win == nil || a.updateBusy || a.updatePackage.URL == "" {
		return
	}
	dir, err := mygo.App.Path(mygo.PathDownloads)
	if err != nil {
		a.banner, a.bannerErr = "Cannot find the Downloads folder: "+err.Error(), true
		return
	}
	pkg, win := a.updatePackage, a.win
	ctx, cancel, seq := a.beginGUIUpdateOperation("downloading", 15*time.Minute)
	a.updateProgress = 0
	a.banner, a.bannerErr = "Downloading GUI "+pkg.Tag+" in the background…", false
	go func() {
		defer cancel()
		path, err := downloadGUIPackage(ctx, newGUIDownloadClient(), pkg, dir, markGUIInternetDownload, func(done, total int64) {
			percent := int(min(done*100/total, 100))
			win.Update(func() {
				if a.updateSeq == seq && a.updatePhase == "downloading" {
					a.updateProgress = percent
					a.banner, a.bannerErr = fmt.Sprintf("Downloading GUI %s… %d%%", pkg.Tag, percent), false
				}
			})
		})
		win.Update(func() {
			if !a.finishGUIUpdateOperation(seq) {
				return
			}
			if err != nil {
				a.updateError = err.Error()
				a.banner, a.bannerErr = "GUI download failed: "+err.Error()+". Click Update to retry.", true
				return
			}
			a.updateError, a.updatePath, a.updateDialogOpen = "", path, true
			a.banner, a.bannerErr = "GUI "+pkg.Tag+" downloaded. Open the package to install it.", false
		})
	}()
}

// openDownloadedGUI is called only after the person explicitly chooses Open.
// Reverify the file off the UI thread before handing it to the system.
func (a *App) openDownloadedGUI(quit bool) {
	if a.win == nil || a.updateBusy || a.updatePath == "" || (quit && (!a.updateDialogOpen || !a.updateQuitConfirm)) {
		return
	}
	pkg, path, win := a.updatePackage, a.updatePath, a.win
	ctx, cancel, seq := a.beginGUIUpdateOperation("opening", 2*time.Minute)
	a.updateError = ""
	a.banner, a.bannerErr = "Checking the downloaded GUI package…", false
	go func() {
		defer cancel()
		err := verifyDownloadedGUIFile(ctx, pkg, path, markGUIInternetDownload)
		win.Update(func() {
			a.finishDownloadedGUIOpen(seq, path, quit, err, mygo.Shell.OpenPath, win.Close)
		})
	}()
}

// finishDownloadedGUIOpen runs on the UI thread. Only a verified package that
// the system accepted for opening can trigger the normal guarded window close.
func (a *App) finishDownloadedGUIOpen(seq uint64, path string, quit bool, verifyErr error, open func(string) error, closeWindow func()) {
	if !a.finishGUIUpdateOperation(seq) {
		return
	}
	if quit && (!a.updateDialogOpen || !a.updateQuitConfirm) {
		a.updateQuitConfirm = false
		a.banner, a.bannerErr = "Opening cancelled. Nothing was installed.", false
		return
	}
	if verifyErr != nil {
		a.updatePath, a.updateError = "", verifyErr.Error()
		a.updateDialogOpen, a.updateQuitConfirm = false, false
		a.banner, a.bannerErr = "Cannot open GUI package: "+verifyErr.Error()+". Click Update to download it again.", true
		return
	}
	if err := open(path); err != nil {
		a.updateError = err.Error()
		a.banner, a.bannerErr = "Cannot open GUI package: "+err.Error(), true
		return
	}
	a.updateError = ""
	a.banner, a.bannerErr = "Package opened. Install it manually, then reopen Golder GUI.", false
	if quit {
		a.updateDialogOpen, a.updateQuitConfirm = false, false
		closeWindow() // Window.Close retains terminal and native-dialog guards.
	}
}

func guiUpdateInstructions(goos string) (openLabel, message string) {
	switch goos {
	case "darwin":
		return "Open disk image", "Open the disk image, quit Golder GUI, then drag the new app to Applications and replace the old one. Reopen Golder afterwards."
	case "windows":
		return "Open installer", "Open the installer and follow its instructions. Quit Golder GUI if requested, then reopen it after installation."
	default:
		return "Open archive", "Open and extract the archive, then install the app using your usual method. If you installed through a package manager, update it through that manager instead."
	}
}

func (a *App) guiUpdateModal(c *ui.Context) {
	if !a.updateDialogOpen && a.updateQuitConfirm {
		a.updateQuitConfirm = false
		if a.updatePhase == "opening" {
			a.cancelGUIUpdate()
			a.banner, a.bannerErr = "Opening cancelled. Nothing was installed.", false
		}
	}
	ui.Modal(c, &a.updateDialogOpen, func() {
		if a.updateQuitConfirm {
			a.guiUpdateQuitConfirmation(c)
			return
		}
		openLabel, message := guiUpdateInstructions(runtime.GOOS)
		ui.Column(c).Width(500).Gap(10).Children(func() {
			ui.Text(c, "GUI "+a.updateTag+" downloaded").FontSize(16).Bold()
			ui.Text(c, "The file was checked against GitHub's release metadata. Nothing has been installed yet.").FontSize(12).Wrap()
			ui.Text(c, message).FontSize(12).Wrap()
			ui.Text(c, "Saved to: "+a.updatePath).FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if subtleButton(c, "Later").Clicked() {
					a.updateDialogOpen = false
				}
				if subtleButton(c, "Show in folder").Clicked() {
					a.updateDialogOpen = false
					mygo.Shell.ShowItemInFolder(a.updatePath)
				}
				if subtleButton(c, openLabel).Clicked() {
					a.updateDialogOpen = false
					a.openDownloadedGUI(false)
				}
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if subtlePrimaryButton(c, "Open and quit", a.updateBusy).Clicked() {
					a.updateError = ""
					a.updateQuitConfirm = true
				}
			})
		})
	})
}

func (a *App) guiUpdateQuitConfirmation(c *ui.Context) {
	ui.Column(c).Width(500).Gap(10).Children(func() {
		ui.Text(c, "Open package and quit?").FontSize(16).Bold()
		ui.Text(c, "Quitting interrupts running tasks in all conversations, cancels pending approvals, and may discard unsaved drafts or file edits. Running terminals may require a separate confirmation.").FontSize(12).Wrap()
		ui.Text(c, "The package will be opened, not installed automatically.").FontSize(12).Wrap()
		if a.updateError != "" {
			ui.Text(c, "Cannot open GUI package: "+a.updateError).FontSize(12).TextColor(c.Theme().Danger).Wrap()
		}
		if a.updateBusy && a.updatePhase == "opening" {
			ui.Text(c, "Checking the downloaded GUI package…").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).Justify(ui.End).Children(func() {
				if subtleButton(c, "Cancel opening").Clicked() {
					a.cancelGUIUpdate()
					a.updateQuitConfirm = false
					a.banner, a.bannerErr = "Opening cancelled. Nothing was installed.", false
				}
			})
			return
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if subtleButton(c, "Back").Clicked() {
				a.updateQuitConfirm = false
				a.updateError = ""
			}
			if subtlePrimaryButton(c, "Confirm open and quit", a.updateBusy).Clicked() {
				a.openDownloadedGUI(true)
			}
		})
	})
}

func (a *App) updateButton(c *ui.Context) ui.Element {
	t := c.Theme()
	tip := "Check for GUI updates"
	switch {
	case a.updatePhase == "downloading":
		tip = fmt.Sprintf("Downloading GUI %s… %d%%", a.updateTag, a.updateProgress)
	case a.updatePhase == "opening":
		tip = "Checking the downloaded GUI package…"
	case a.updateBusy:
		tip = "Checking for GUI updates…"
	case a.updatePath != "":
		tip = "Open downloaded GUI " + a.updateTag
	case a.updateTag != "":
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
