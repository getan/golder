package mygoui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

var larkDomainOrder = []string{"docs", "drive", "im", "sheets", "base"}

func (a *App) loadExtensionSettings() {
	settings, err := a.ctrl.ExtensionSettings()
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	a.extension = settings
	a.extensionSources = a.ctrl.ExtensionSources()
	a.extensionDomains = map[string]bool{}
	for _, domain := range settings.Domains {
		a.extensionDomains[domain] = true
	}
	a.extensionEdit = appsession.LarkExtensionEdit{
		LarkCLI: settings.LarkCLI, ProjectCLI: settings.ProjectCLI,
		AppID: settings.AppID, CredentialName: settings.CredentialName,
		Domains: append([]string(nil), settings.Domains...), ProjectHost: settings.ProjectHost,
	}
	a.extensionErr = ""
}

func (a *App) selectedLarkDomains() []string {
	var out []string
	for _, domain := range larkDomainOrder {
		if a.extensionDomains[domain] {
			out = append(out, domain)
		}
	}
	return out
}

func bundledLarkHelper() (source, checksum string, err error) {
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil || resources == "" {
		return "", "", errors.New("cannot locate the app resources")
	}
	name := "golder-lark"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source = filepath.Join(resources, "extensions", "lark", name)
	if info, statErr := os.Stat(source); statErr != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("this development build does not include the managed golder-lark helper")
	}
	raw, err := os.ReadFile(source + ".sha256")
	if err != nil || len(raw) > 4096 {
		return "", "", errors.New("the bundled golder-lark checksum is missing")
	}
	return source, string(raw), nil
}

func (a *App) installLarkPackage() {
	if a.extensionBusy {
		return
	}
	version := strings.TrimSpace(a.extension.BundledVersion)
	if version == "" {
		a.extensionErr = "Lark helper version is unavailable; reload Settings and try again."
		return
	}
	source, checksum, err := bundledLarkHelper()
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	a.extensionBusy, a.extensionErr, a.extensionNote = true, "", "Installing verified helper…"
	win, ctrl := a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := ctrl.InstallLarkPackage(ctx, source, checksum, version)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			if a.ctrl != ctrl {
				return
			}
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			a.extensionNote = "Lark helper installed. Configure the official CLIs and application next."
			a.loadExtensionSettings()
		})
	}()
}

func (a *App) updateLarkPackage() {
	if a.extensionBusy {
		return
	}
	version := strings.TrimSpace(a.extension.BundledVersion)
	if version == "" {
		a.extensionErr = "Lark helper version is unavailable; reload Settings and try again."
		return
	}
	source, checksum, err := bundledLarkHelper()
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	a.extensionBusy, a.extensionErr = true, ""
	a.extensionNote = "Installing the bundled helper and rebinding the existing Source…"
	configured := a.extension.Configured
	win, ctrl := a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if configured {
			err = ctrl.UpdateLarkPackage(ctx, source, checksum, version)
		} else {
			err = ctrl.InstallLarkPackage(ctx, source, checksum, version)
		}
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			if a.ctrl != ctrl {
				return
			}
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			if configured {
				a.extensionNote = "Helper updated and Source rebound. Login state and named credentials were preserved; review and enable the new helper."
			} else {
				a.extensionNote = "Bundled helper installed. Save configuration to create the Source."
			}
			a.loadExtensionSettings()
		})
	}()
}

func (a *App) chooseExtensionCLI(project bool) {
	win := a.win
	go func() {
		title := "Choose the official lark-cli executable"
		if project {
			title = "Choose the official Project/meegle executable"
		}
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: title, ButtonLabel: "Choose", Multiple: false,
			ShowHiddenFiles: true, TreatPackagesAsDirectories: true,
		})
		if win == nil {
			return
		}
		win.Update(func() {
			if err != nil {
				a.extensionErr = err.Error()
				return
			}
			if len(paths) == 0 {
				return
			}
			if project {
				a.extensionEdit.ProjectCLI = paths[0]
			} else {
				a.extensionEdit.LarkCLI = paths[0]
			}
		})
	}()
}

func (a *App) detectLarkCLIs() {
	larkCLI, projectCLI, larkFound, projectFound := a.ctrl.DetectLarkCLIs(a.extensionEdit.LarkCLI, a.extensionEdit.ProjectCLI)
	found := 0
	if larkFound {
		a.extensionEdit.LarkCLI = larkCLI
		found++
	}
	if projectFound {
		a.extensionEdit.ProjectCLI = projectCLI
		found++
	}
	if found == 0 {
		a.extensionNote = "No installed native CLIs were found. Use the install buttons; npm and Node are not required."
	} else {
		a.extensionNote = fmt.Sprintf("Detected %d installed native CLI(s). Review the paths, then save configuration.", found)
	}
}

func (a *App) installOfficialLarkCLI(project bool) {
	if a.extensionBusy {
		return
	}
	name := "lark-cli"
	if project {
		name = "Project CLI"
	}
	a.extensionBusy, a.extensionErr = true, ""
	a.extensionNote = "Downloading and verifying the official " + name + " native binary…"
	win, ctrl := a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		path, err := ctrl.InstallOfficialLarkCLI(ctx, project)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			if a.ctrl != ctrl {
				return
			}
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			if project {
				a.extensionEdit.ProjectCLI = path
			} else {
				a.extensionEdit.LarkCLI = path
			}
			a.extensionNote = name + " installed and selected. Save configuration to apply it."
		})
	}()
}

func (a *App) configureLark() {
	if a.extensionBusy {
		return
	}
	a.extensionEdit.Domains = a.selectedLarkDomains()
	a.extensionBusy, a.extensionErr, a.extensionNote = true, "", "Saving disabled configuration…"
	edit := a.extensionEdit
	win, ctrl := a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := ctrl.ConfigureLark(ctx, edit)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			if a.ctrl != ctrl {
				return
			}
			a.extensionEdit.NewAppSecret = ""
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			a.extensionLarkVerified, a.extensionProjectVerified = false, false
			a.extensionNote = "Configuration saved disabled. Review the capabilities, then enable."
			a.loadExtensionSettings()
		})
	}()
}

func (a *App) setLarkEnabled(enabled bool) {
	a.runExtensionOperation(map[bool]string{true: "Approving and enabling…", false: "Disabling…"}[enabled], func(_ context.Context, ctrl *appsession.Controller) error {
		if enabled {
			return ctrl.EnableLark()
		}
		return ctrl.DisableLark()
	})
}

func (a *App) testLark() {
	a.runExtensionOperation("Testing MCP initialization…", func(ctx context.Context, ctrl *appsession.Controller) error {
		return ctrl.TestLark(ctx)
	})
}

func (a *App) authorizeLark(service string) {
	if a.extensionBusy {
		return
	}
	if service == "project" && !projectAuthorizationConfigured(a.extension, a.extensionEdit) {
		a.extensionErr = "Save the Project CLI and host configuration, then approve and enable the updated source before authorizing."
		return
	}
	if a.hub == nil {
		a.extensionErr = "Private authorization is unavailable."
		return
	}
	release, err := a.hub.BeginPrivateAuthorization()
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.extensionCancel = cancel
	if service == "project" {
		a.extensionProjectVerified = false
	} else {
		a.extensionLarkVerified = false
	}
	a.extensionBusy, a.extensionErr = true, ""
	a.extensionNote = "Preparing authorization…"
	a.extensionAuthURL = ""
	a.extensionAuthCode = ""
	a.extensionAuthQR = nil
	win, ctrl := a.win, a.ctrl
	go func() {
		defer cancel()
		defer release()
		err := ctrl.AuthorizeLark(ctx, service, func(event appsession.ExtensionSetupEvent) {
			if win == nil {
				return
			}
			win.Update(func() {
				if a.ctrl != ctrl || ctx.Err() != nil || a.extensionCancel == nil {
					return
				}
				_ = a.applyExtensionSetupEvent(event)
			})
		})
		if win == nil {
			return
		}
		done := make(chan struct{})
		win.Update(func() {
			defer close(done)
			a.extensionBusy = false
			a.extensionCancel = nil
			a.extensionAuthURL = ""
			a.extensionAuthCode = ""
			a.extensionAuthQR = nil
			if a.ctrl != ctrl {
				return
			}
			eventErr := a.extensionErr
			a.loadExtensionSettings()
			refreshErr := a.extensionErr
			if err != nil {
				if eventErr != "" {
					a.extensionErr = eventErr
				} else {
					a.extensionErr = err.Error()
				}
				a.extensionNote = ""
				return
			}
			if refreshErr != "" {
				a.extensionNote = "Authorization verified, but status refresh failed."
				return
			}
			a.extensionErr = ""
			if service == "project" {
				a.extensionProjectVerified = true
			} else {
				a.extensionLarkVerified = true
			}
			a.extensionNote = "Authorization verified."
		})
		<-done // hide private authorization before allowing new agent turns
	}()
}

func projectAuthorizationConfigured(settings appsession.LarkExtensionSettings, edit appsession.LarkExtensionEdit) bool {
	saved, current := strings.TrimSpace(settings.SavedProjectCLI), strings.TrimSpace(edit.ProjectCLI)
	if saved == "" || current == "" || filepath.Clean(saved) != filepath.Clean(current) {
		return false
	}
	host := strings.TrimSpace(edit.ProjectHost)
	if host == "" {
		host = "project.feishu.cn"
	}
	return strings.EqualFold(host, strings.TrimSpace(settings.SavedProjectHost))
}

func sameLarkPath(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" || right == "" {
		return left == right
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func (a *App) larkNextAction(helperReady bool) string {
	s, edit := a.extension, a.extensionEdit
	if !s.Installed {
		return "install"
	}
	if s.UpdateAvailable {
		return "update"
	}
	if strings.TrimSpace(edit.LarkCLI) == "" {
		return "install-cli"
	}
	if !s.Configured || !sameLarkPath(edit.LarkCLI, s.SavedLarkCLI) || !sameLarkPath(edit.ProjectCLI, s.SavedProjectCLI) ||
		strings.TrimSpace(edit.AppID) != strings.TrimSpace(s.AppID) || strings.TrimSpace(edit.CredentialName) != strings.TrimSpace(s.CredentialName) ||
		strings.TrimSpace(edit.NewAppSecret) != "" || larkApplicationStatus(s) != "Configured" ||
		(strings.TrimSpace(edit.ProjectCLI) != "" && !projectAuthorizationConfigured(s, edit)) {
		return "save"
	}
	if a.extensionDomains != nil {
		for _, name := range larkDomainOrder {
			saved := false
			for _, domain := range s.Domains {
				if domain == name {
					saved = true
					break
				}
			}
			if a.extensionDomains[name] != saved {
				return "save"
			}
		}
	}
	if !s.Enabled {
		return "enable"
	}
	if !helperReady {
		return ""
	}
	// The persisted source status combines both services. Do not infer which
	// service was verified in an earlier GUI session.
	if !a.extensionLarkVerified && !a.extensionProjectVerified && s.Authentication == "verified" {
		return "test"
	}
	if !a.extensionLarkVerified {
		return "lark"
	}
	if projectAuthorizationConfigured(s, edit) && !a.extensionProjectVerified {
		return "project"
	}
	return "test"
}

func larkWizardButton(c *ui.Context, label string, primary, disabled bool) ui.Element {
	if primary {
		return subtlePrimaryButton(c, label, disabled)
	}
	return subtleButton(c, label, disabled)
}

func (a *App) applyExtensionSetupEvent(event appsession.ExtensionSetupEvent) string {
	switch event.Type {
	case "notice":
		a.extensionNote = event.Message
	case "authorization":
		a.extensionAuthURL = event.VerificationURL
		a.extensionAuthCode = event.UserCode
		a.extensionAuthQR = authorizationQR(event.VerificationURL)
		if a.extensionAuthQR == nil {
			a.extensionErr = "Could not display the authorization QR code."
		} else {
			a.extensionErr = ""
		}
		a.extensionNote = "Scan the QR code with your phone and complete authorization; Golder is waiting for verified login."
		return event.VerificationURL
	case "failure":
		a.extensionAuthURL = ""
		a.extensionAuthCode = ""
		a.extensionAuthQR = nil
		a.extensionNote = ""
		a.extensionErr = extensionSetupFailureText(event)
	case "complete":
		a.extensionAuthURL = ""
		a.extensionAuthCode = ""
		a.extensionAuthQR = nil
		a.extensionErr = ""
		a.extensionNote = "Authorization verified."
	}
	return ""
}

func extensionSetupFailureText(event appsession.ExtensionSetupEvent) string {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = "Authorization failed."
	}
	if event.Code != "" && event.Phase != "" {
		message += " [" + event.Code + " · " + event.Phase + "]"
	}
	if event.Retryable {
		message += " Retry is safe."
	}
	return message
}

func (a *App) runExtensionOperation(note string, run func(context.Context, *appsession.Controller) error) {
	if a.extensionBusy {
		return
	}
	a.extensionBusy, a.extensionErr, a.extensionNote = true, "", note
	win, ctrl := a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := run(ctx, ctrl)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			if a.ctrl != ctrl {
				return
			}
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			a.extensionNote = "Extension operation completed."
			a.loadExtensionSettings()
		})
	}()
}

func (a *App) resetLark() {
	if a.extensionConfirm != "reset" {
		a.extensionConfirm = "reset"
		return
	}
	a.extensionConfirm = ""
	a.extensionLarkVerified, a.extensionProjectVerified = false, false
	a.runExtensionOperation("Clearing source-scoped login state…", func(_ context.Context, ctrl *appsession.Controller) error { return ctrl.ResetLark() })
}

func (a *App) uninstallLark() {
	if a.extensionConfirm != "uninstall" {
		a.extensionConfirm = "uninstall"
		return
	}
	a.extensionConfirm = ""
	a.extensionLarkVerified, a.extensionProjectVerified = false, false
	a.runExtensionOperation("Removing source and managed helper…", func(_ context.Context, ctrl *appsession.Controller) error { return ctrl.UninstallLark() })
}

func (a *App) setGenericExtension(source appsession.ExtensionSourceView, enabled bool) {
	if a.status.Running {
		a.extensionErr = "Finish the current turn before changing an extension."
		return
	}
	key := source.Kind + ":" + source.ID
	if enabled && a.extensionSourceConfirm != key {
		if source.Kind == "mcp" {
			edit, err := a.ctrl.PrepareMCPEdit(source.ID)
			if err != nil {
				a.extensionErr = err.Error()
				return
			}
			a.mcpApproval = &mcpRemoveState{edit: edit}
		}
		a.extensionSourceConfirm = key
		a.extensionNote = "Review the executable endpoint and capabilities shown for " + key + ", then click Confirm enable."
		return
	}
	if enabled && source.Kind == "mcp" {
		// The detailed review card owns the actual confirmation button.
		a.extensionSourceConfirm, a.mcpApproval = "", nil
		return
	}
	a.extensionSourceConfirm = ""
	a.mcpApproval = nil
	a.runExtensionOperation("Updating "+source.Kind+":"+source.ID+"…", func(_ context.Context, ctrl *appsession.Controller) error {
		return ctrl.SetExtensionEnabled(source.Kind, source.ID, enabled)
	})
}

func (a *App) testGenericExtension(source appsession.ExtensionSourceView) {
	a.runExtensionOperation("Testing "+source.Kind+":"+source.ID+"…", func(ctx context.Context, ctrl *appsession.Controller) error {
		return ctrl.TestExtension(ctx, source.Kind, source.ID)
	})
}

func (a *App) extensionsSettingsView(c *ui.Context) {
	theme := c.Theme()
	ui.Column(c).Padding(18, 20, 24, 20).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, "Extensions").FontSize(16).FontWeight(600)
				ui.Text(c, "Install, review, authorize and diagnose extension processes without editing TOML.").FontSize(12).TextColor(theme.TextMuted)
			})
			if subtleButton(c, "+ Add MCP", a.extensionBusy || a.mcpAdd != nil && a.mcpAdd.busy).Clicked() {
				a.mcpAdd = newMCPAddForm()
			}
			if subtleButton(c, "Refresh", a.extensionBusy).Clicked() {
				a.loadExtensionSettings()
			}
		})

		if a.mcpAdd != nil {
			a.mcpAddView(c)
		}
		if a.mcpOAuth != nil {
			a.mcpAuthorizationView(c)
		}
		a.larkExtensionCard(c)

		if len(a.extensionSources) > 0 {
			ui.Text(c, "Configured sources").FontWeight(600).Padding(8, 0, 0, 0)
		}
		for _, source := range a.extensionSources {
			source := source
			if source.Kind == "mcp" && source.ID == "lark" {
				continue
			}
			ui.Row(c.Key(source.Kind+":"+source.ID)).Padding(10, 12).Gap(8).Radius(9).Background(theme.Surface).Border(1, theme.Border).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, source.Kind+":"+source.ID).FontWeight(600)
					ui.Text(c, source.Type+" · "+source.State+" · "+source.Authentication).FontSize(11).TextColor(theme.TextMuted)
					ui.Text(c, source.Endpoint).FontSize(10).TextColor(theme.TextMuted).SingleLine().Ellipsis("…")
					ui.Text(c, fmt.Sprintf("Trust: %s · network: %t · private state: %t · executables: %d · tools: %d",
						map[bool]string{true: "approved", false: "needs review"}[source.Trusted], source.Network, source.StateDir, source.Executables, source.Tools)).FontSize(10).TextColor(theme.TextMuted)
				})
				if subtleButton(c, "Test", a.extensionBusy || !source.Enabled).Clicked() {
					a.testGenericExtension(source)
				}
				if source.Kind == "mcp" && strings.Contains(source.Type, "http") {
					if subtleButton(c, "Authorize", a.extensionBusy || !source.Enabled || !source.Trusted).Clicked() {
						a.startMCPAuthorization(source.ID)
					}
				}
				if source.Kind == "mcp" {
					if subtleButton(c, "Tools", a.extensionBusy).Clicked() {
						if a.mcpToolsID == source.ID {
							a.mcpToolsID = ""
						} else {
							a.mcpToolsID = source.ID
						}
					}
					if subtleButton(c, "Edit", a.extensionBusy || a.mcpAdd != nil && a.mcpAdd.busy).Clicked() {
						a.beginMCPEdit(source.ID)
					}
					removeLabel := "Remove"
					if a.mcpRemove != nil && a.mcpRemove.edit.ID == source.ID {
						removeLabel = "Confirm remove"
					}
					if subtleButton(c, removeLabel, a.extensionBusy).Clicked() {
						a.confirmMCPRemove(source.ID)
					}
				}
				label := "Enable"
				if source.Enabled {
					label = "Disable"
				} else if a.extensionSourceConfirm == source.Kind+":"+source.ID {
					if source.Kind == "mcp" {
						label = "Cancel review"
					} else {
						label = "Confirm enable"
					}
				}
				if subtleButton(c, label, a.extensionBusy).Clicked() {
					a.setGenericExtension(source, !source.Enabled)
				}
			})
			if source.Kind == "mcp" && a.mcpToolsID == source.ID {
				a.mcpToolsView(c, source.ID)
			}
			if source.Kind == "mcp" && a.mcpApproval != nil && a.mcpApproval.edit.ID == source.ID {
				a.mcpApprovalView(c)
			}
		}
	})
}

func (a *App) larkExtensionCard(c *ui.Context) {
	theme := c.Theme()
	bundledVersion, installedVersion, activeVersion := larkHelperVersions(a.extension)
	helperReady := larkHelperReady(a.extension)
	next := a.larkNextAction(helperReady)
	statusColor := theme.TextMuted
	if a.extension.Enabled && a.extension.State == "ready" {
		statusColor = theme.Success
	} else if a.extension.LastError != "" {
		statusColor = theme.Danger
	}
	ui.Column(c).Padding(14).Gap(12).Radius(12).Background(theme.Surface).Border(1, theme.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, "Lark + Project").FontSize(15).FontWeight(600)
				ui.Text(c, a.extension.Description).FontSize(11).TextColor(theme.TextMuted)
			})
			ui.Text(c, a.extension.State).FontSize(11).TextColor(statusColor)
		})
		if hint := map[string]string{
			"install": "Install the bundled helper.", "update": "Update the helper.",
			"install-cli": "Detect or install lark-cli.", "save": "Review the paths and application credentials, then save.",
			"enable": "Review and enable the saved configuration.", "lark": "Authorize your Lark user.",
			"project": "Authorize your Project user.", "test": "Test the connection; authorize either service if needed.",
		}[next]; hint != "" {
			ui.Text(c, "Next: "+hint).FontSize(11).TextColor(theme.TextMuted).Wrap()
		}

		if !a.extension.Installed {
			ui.Text(c, "1. Install the signed, matching golder-lark helper. Nothing is configured or executed during discovery.").FontSize(12)
			ui.Text(c, "Bundled: "+bundledVersion).FontSize(11).TextColor(theme.TextMuted)
			installLabel := "Install"
			if a.extension.Configured {
				installLabel = "Install & rebind"
			}
			if subtlePrimaryButton(c, installLabel, a.extensionBusy).Clicked() {
				if a.extension.Configured {
					a.updateLarkPackage()
				} else {
					a.installLarkPackage()
				}
			}
			a.extensionStatus(c)
			return
		}

		ui.Text(c, "1. Package").FontWeight(600)
		ui.Text(c, "Bundled: "+bundledVersion).FontSize(12).TextColor(theme.TextMuted)
		ui.Text(c, "Installed latest: "+installedVersion).FontSize(12).TextColor(theme.TextMuted)
		ui.Text(c, "Active Source: "+activeVersion).FontSize(12).TextColor(map[bool]ui.Color{true: theme.Success, false: theme.Warning}[helperReady])
		if a.extension.UpdateAvailable {
			ui.Text(c, "Update the managed helper before authorization. Existing login state and named credentials will be preserved.").FontSize(11).TextColor(theme.Warning).Wrap()
			if subtlePrimaryButton(c, "Update helper", a.extensionBusy).Clicked() {
				a.updateLarkPackage()
			}
		}

		ui.Text(c, "2. Application and official CLIs").FontWeight(600).Padding(6, 0, 0, 0)
		extensionPathField(c, "Official lark-cli", &a.extensionEdit.LarkCLI, func() { a.chooseExtensionCLI(false) })
		extensionPathField(c, "Project/meegle CLI (optional)", &a.extensionEdit.ProjectCLI, func() { a.chooseExtensionCLI(true) })
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if subtleButton(c, "Detect installed CLIs", a.extensionBusy).Clicked() {
				a.detectLarkCLIs()
			}
			if larkWizardButton(c, "Install lark-cli", next == "install-cli", a.extensionBusy).Clicked() {
				a.installOfficialLarkCLI(false)
			}
			if subtleButton(c, "Install Project CLI", a.extensionBusy).Clicked() {
				a.installOfficialLarkCLI(true)
			}
		})
		ui.Text(c, "Installs pinned official native releases with SHA-256/SHA-512 verification; npm and Node are not required.").FontSize(10.5).TextColor(theme.TextMuted).Wrap()
		ui.Field(c, "Application ID", func() { ui.TextInput(c, &a.extensionEdit.AppID).FillWidth().Placeholder("cli_…") })
		ui.Field(c, "App Secret credential name", func() {
			ui.TextInput(c, &a.extensionEdit.CredentialName).FillWidth().Placeholder("lark-app-secret")
		})
		ui.Field(c, "New App Secret", func() {
			ui.TextInput(c, &a.extensionEdit.NewAppSecret).Password().FillWidth().Placeholder("Leave blank to keep the named credential")
		})
		ui.Text(c, "App Secrets use the same AES-256-GCM encrypted credential store.").FontSize(11).TextColor(theme.TextMuted)
		ui.Field(c, "Project host", func() {
			ui.TextInput(c, &a.extensionEdit.ProjectHost).FillWidth().Placeholder("project.feishu.cn")
		})
		ui.Text(c, "Services").FontSize(12).FontWeight(600)
		ui.Row(c).Gap(10).Wrap().Children(func() {
			for _, domain := range larkDomainOrder {
				domain := domain
				checked := a.extensionDomains[domain]
				if ui.Checkbox(c, &checked, domain).Changed() {
					a.extensionDomains[domain] = checked
				}
			}
		})
		if larkWizardButton(c, "Save configuration", next == "save", a.extensionBusy).Clicked() {
			a.configureLark()
		}

		ui.Text(c, "3. Review and enable").FontWeight(600).Padding(6, 0, 0, 0)
		ui.Text(c, "Runs a checksum-verified helper with network access, a private HOME, and only the official CLI paths shown above. Business writes still require normal tool approval.").
			FontSize(11).TextColor(theme.TextMuted).MaxWidth(650)
		if a.extension.Configured {
			if !a.extension.Enabled {
				if larkWizardButton(c, "Approve & enable", next == "enable", a.extensionBusy || !helperReady || next == "save" || next == "install-cli").Clicked() {
					a.setLarkEnabled(true)
				}
			} else if subtleButton(c, "Disable", a.extensionBusy).Clicked() {
				a.setLarkEnabled(false)
			}
		}

		ui.Text(c, "4. Personal authorization").FontWeight(600).Padding(6, 0, 0, 0)
		ui.Text(c, "Application: "+larkApplicationStatus(a.extension)).FontSize(11).TextColor(theme.TextMuted)
		ui.Text(c, "User authorization: "+larkAuthorizationStatus(a.extension.Authentication)).FontSize(11).TextColor(theme.TextMuted)
		ui.Text(c, "Lark and Project have separate personal authorizations; the source status above is shared.").FontSize(10.5).TextColor(theme.TextMuted).Wrap()
		projectConfigured := projectAuthorizationConfigured(a.extension, a.extensionEdit)
		if strings.TrimSpace(a.extensionEdit.ProjectCLI) != "" && !projectConfigured {
			ui.Text(c, "Save the Project CLI and host, then approve the updated configuration before authorizing Project.").FontSize(11).TextColor(theme.Warning).Wrap()
		}
		unsaved := next == "save" || next == "install-cli"
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if larkWizardButton(c, "Authorize Lark", next == "lark", a.extensionBusy || !a.extension.Enabled || !helperReady || unsaved).Clicked() {
				a.authorizeLark("lark")
			}
			if larkWizardButton(c, "Authorize Project", next == "project", a.extensionBusy || !a.extension.Enabled || !helperReady || !projectConfigured || unsaved).Clicked() {
				a.authorizeLark("project")
			}
			if larkWizardButton(c, "Test connection", next == "test", a.extensionBusy || !a.extension.Enabled || !helperReady || unsaved).Clicked() {
				a.testLark()
			}
			if a.extensionBusy && a.extensionCancel != nil {
				if subtleButton(c, "Cancel authorization").Clicked() {
					a.extensionCancel()
				}
			}
		})
		if a.extensionBusy && a.extensionAuthURL != "" {
			if a.extensionAuthCode != "" {
				ui.Text(c, "Enter this authorization code on the Project page: "+a.extensionAuthCode).FontSize(12).Wrap()
			}
			if a.extensionAuthQR != nil {
				ui.Text(c, "Scan with your phone to authorize.").FontSize(12).Wrap()
				ui.Image(c, a.extensionAuthQR).Size(200, 200)
			}
		}

		a.extensionStatus(c)
		ui.Row(c).Gap(8).Padding(8, 0, 0, 0).Children(func() {
			resetLabel := "Reset local login"
			if a.extensionConfirm == "reset" {
				resetLabel = "Confirm reset"
			}
			if subtleButton(c, resetLabel, a.extensionBusy || !a.extension.Configured).Clicked() {
				a.resetLark()
			}
			uninstallLabel := "Uninstall"
			if a.extensionConfirm == "uninstall" {
				uninstallLabel = "Confirm uninstall"
			}
			if subtleButton(c, uninstallLabel, a.extensionBusy).Clicked() {
				a.uninstallLark()
			}
		})
	})
}

func larkApplicationStatus(extension appsession.LarkExtensionSettings) string {
	if extension.Configured && strings.TrimSpace(extension.AppID) != "" && extension.CredentialAvailable {
		return "Configured"
	}
	return "Missing or incomplete"
}

func larkAuthorizationStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "verified":
		return "Verified"
	case "pending":
		return "Pending"
	case "verification-failed":
		return "Verification failed"
	case "required":
		return "Required"
	case "configured":
		return "Not verified"
	case "not-required":
		return "Not verified"
	default:
		return "Unknown"
	}
}

func larkHelperVersions(extension appsession.LarkExtensionSettings) (bundled, installed, active string) {
	bundled = strings.TrimSpace(extension.BundledVersion)
	if bundled == "" {
		bundled = "Unknown"
	}
	installed = strings.TrimSpace(extension.InstalledVersion)
	if installed == "" {
		installed = strings.TrimSpace(extension.Version)
	}
	active = strings.TrimSpace(extension.ActiveVersion)
	if active == "" && extension.Configured {
		active = strings.TrimSpace(extension.Version)
	}
	if installed == "" {
		installed = "Not installed"
	}
	if active == "" {
		active = "Not configured"
	}
	return bundled, installed, active
}

func larkHelperReady(extension appsession.LarkExtensionSettings) bool {
	bundled, _, active := larkHelperVersions(extension)
	if !extension.Configured || strings.TrimSpace(extension.BundledVersion) == "" {
		return false
	}
	if bundled == "dev" {
		return true
	}
	return extension.ActiveManaged && active == bundled && !extension.UpdateAvailable
}

func extensionPathField(c *ui.Context, label string, value *string, choose func()) {
	ui.Field(c, label, func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.TextInput(c, value).Grow(1).Placeholder("Choose an absolute native executable")
			if subtleButton(c, "Choose…").Clicked() {
				choose()
			}
		})
	})
}

func (a *App) extensionStatus(c *ui.Context) {
	theme := c.Theme()
	if a.extensionErr != "" {
		ui.Text(c, a.extensionErr).FontSize(12).TextColor(theme.Danger).Wrap()
	} else if a.extensionNote != "" {
		ui.Text(c, a.extensionNote).FontSize(12).TextColor(theme.TextMuted).Wrap()
	}
	if a.extension.LastError != "" && a.extension.LastError != a.extensionErr {
		ui.Text(c, a.extension.LastError).FontSize(11).TextColor(theme.Danger).Wrap()
	}
}
