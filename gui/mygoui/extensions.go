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

func extensionPackageVersion(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "unknown" {
		return "dev"
	}
	version = strings.ReplaceAll(version, "+", "-")
	return version
}

func (a *App) installLarkPackage() {
	if a.extensionBusy {
		return
	}
	source, checksum, err := bundledLarkHelper()
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	a.extensionBusy, a.extensionErr, a.extensionNote = true, "", "Installing verified helper…"
	version := extensionPackageVersion(a.ctrl.Version())
	win := a.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := a.ctrl.InstallLarkPackage(ctx, source, checksum, version)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			a.extensionNote = "Lark helper installed. Configure the official CLIs and application next."
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

func (a *App) configureLark() {
	if a.extensionBusy {
		return
	}
	a.extensionEdit.Domains = a.selectedLarkDomains()
	a.extensionBusy, a.extensionErr, a.extensionNote = true, "", "Saving disabled configuration…"
	edit := a.extensionEdit
	win := a.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := a.ctrl.ConfigureLark(ctx, edit)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			a.extensionEdit.NewAppSecret = ""
			if err != nil {
				a.extensionErr, a.extensionNote = err.Error(), ""
				return
			}
			a.extensionNote = "Configuration saved disabled. Review the capabilities, then enable."
			a.loadExtensionSettings()
		})
	}()
}

func (a *App) setLarkEnabled(enabled bool) {
	a.runExtensionOperation(map[bool]string{true: "Approving and enabling…", false: "Disabling…"}[enabled], func(context.Context) error {
		if enabled {
			return a.ctrl.EnableLark()
		}
		return a.ctrl.DisableLark()
	})
}

func (a *App) testLark() {
	a.runExtensionOperation("Testing MCP initialization…", func(ctx context.Context) error {
		return a.ctrl.TestLark(ctx)
	})
}

func (a *App) authorizeLark(service string) {
	if a.extensionBusy {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.extensionCancel = cancel
	a.extensionBusy, a.extensionErr = true, ""
	a.extensionNote = "Preparing authorization…"
	a.extensionAuthURL = ""
	win := a.win
	go func() {
		err := a.ctrl.AuthorizeLark(ctx, service, func(event appsession.ExtensionSetupEvent) {
			if win == nil {
				return
			}
			win.Update(func() {
				if openURL := a.applyExtensionSetupEvent(event); openURL != "" {
					_ = mygo.Shell.OpenExternal(openURL)
				}
			})
		})
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
			a.extensionCancel = nil
			a.extensionAuthURL = ""
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
			a.extensionNote = "Authorization verified."
		})
	}()
}

func (a *App) applyExtensionSetupEvent(event appsession.ExtensionSetupEvent) string {
	switch event.Type {
	case "notice":
		a.extensionNote = event.Message
	case "authorization":
		a.extensionAuthURL = event.VerificationURL
		a.extensionErr = ""
		a.extensionNote = "Authorization page opened. Complete consent in the browser; Golder is waiting for verified login."
		return event.VerificationURL
	case "failure":
		a.extensionAuthURL = ""
		a.extensionNote = ""
		a.extensionErr = extensionSetupFailureText(event)
	case "complete":
		a.extensionAuthURL = ""
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
		message += " Start a new authorization when ready."
	}
	return message
}

func (a *App) runExtensionOperation(note string, run func(context.Context) error) {
	if a.extensionBusy {
		return
	}
	a.extensionBusy, a.extensionErr, a.extensionNote = true, "", note
	win := a.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := run(ctx)
		if win == nil {
			return
		}
		win.Update(func() {
			a.extensionBusy = false
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
	a.runExtensionOperation("Clearing source-scoped login state…", func(context.Context) error { return a.ctrl.ResetLark() })
}

func (a *App) uninstallLark() {
	if a.extensionConfirm != "uninstall" {
		a.extensionConfirm = "uninstall"
		return
	}
	a.extensionConfirm = ""
	a.runExtensionOperation("Removing source and managed helper…", func(context.Context) error { return a.ctrl.UninstallLark() })
}

func (a *App) setGenericExtension(source appsession.ExtensionSourceView, enabled bool) {
	key := source.Kind + ":" + source.ID
	if enabled && a.extensionSourceConfirm != key {
		a.extensionSourceConfirm = key
		a.extensionNote = "Review the executable endpoint and capabilities shown for " + key + ", then click Confirm enable."
		return
	}
	a.extensionSourceConfirm = ""
	a.runExtensionOperation("Updating "+source.Kind+":"+source.ID+"…", func(context.Context) error {
		return a.ctrl.SetExtensionEnabled(source.Kind, source.ID, enabled)
	})
}

func (a *App) testGenericExtension(source appsession.ExtensionSourceView) {
	a.runExtensionOperation("Testing "+source.Kind+":"+source.ID+"…", func(ctx context.Context) error {
		return a.ctrl.TestExtension(ctx, source.Kind, source.ID)
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
			if ui.Button(c, "Refresh").Disabled(a.extensionBusy).Clicked() {
				a.loadExtensionSettings()
			}
		})

		a.larkExtensionCard(c)

		if len(a.extensionSources) > 0 {
			ui.Text(c, "Configured sources").FontWeight(600).Padding(8, 0, 0, 0)
		}
		for _, source := range a.extensionSources {
			source := source
			if source.Kind == "mcp" && source.ID == "lark" {
				continue
			}
			ui.Row(c.Key(source.Kind+":"+source.ID)).Padding(10, 12).Gap(8).Radius(9).Background(theme.Surface).Border(1, theme.Border).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, source.Kind+":"+source.ID).FontWeight(600)
					ui.Text(c, source.Type+" · "+source.State+" · "+source.Authentication).FontSize(11).TextColor(theme.TextMuted)
					ui.Text(c, source.Endpoint).FontSize(10).TextColor(theme.TextMuted).SingleLine().Ellipsis("…")
					ui.Text(c, fmt.Sprintf("Trust: %s · network: %t · private state: %t · executables: %d · tools: %d",
						map[bool]string{true: "approved", false: "needs review"}[source.Trusted], source.Network, source.StateDir, source.Executables, source.Tools)).FontSize(10).TextColor(theme.TextMuted)
				})
				if ui.Button(c, "Test").Disabled(a.extensionBusy || !source.Enabled).Clicked() {
					a.testGenericExtension(source)
				}
				label := "Enable"
				if source.Enabled {
					label = "Disable"
				} else if a.extensionSourceConfirm == source.Kind+":"+source.ID {
					label = "Confirm enable"
				}
				if ui.Button(c, label).Disabled(a.extensionBusy).Clicked() {
					a.setGenericExtension(source, !source.Enabled)
				}
			})
		}
	})
}

func (a *App) larkExtensionCard(c *ui.Context) {
	theme := c.Theme()
	guiVersion := ""
	if a.ctrl != nil {
		guiVersion = a.ctrl.Version()
	}
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

		if !a.extension.Installed {
			ui.Text(c, "1. Install the signed, matching golder-lark helper. Nothing is configured or executed during discovery.").FontSize(12)
			if ui.PrimaryButton(c, "Install").Disabled(a.extensionBusy).Clicked() {
				a.installLarkPackage()
			}
			a.extensionStatus(c)
			return
		}

		ui.Text(c, "1. Package").FontWeight(600)
		ui.Text(c, "Installed · "+a.extension.Version).FontSize(12).TextColor(theme.Success)
		if warning := extensionVersionWarning(guiVersion, a.extension.Version); warning != "" {
			ui.Text(c, warning).FontSize(11).TextColor(theme.Warning).Wrap()
		}

		ui.Text(c, "2. Application and official CLIs").FontWeight(600).Padding(6, 0, 0, 0)
		extensionPathField(c, "Official lark-cli", &a.extensionEdit.LarkCLI, func() { a.chooseExtensionCLI(false) })
		extensionPathField(c, "Project/meegle CLI (optional)", &a.extensionEdit.ProjectCLI, func() { a.chooseExtensionCLI(true) })
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
		if ui.Button(c, "Save configuration").Disabled(a.extensionBusy).Clicked() {
			a.configureLark()
		}

		ui.Text(c, "3. Review and enable").FontWeight(600).Padding(6, 0, 0, 0)
		ui.Text(c, "Runs a checksum-verified helper with network access, a private HOME, and only the official CLI paths shown above. Business writes still require normal tool approval.").
			FontSize(11).TextColor(theme.TextMuted).MaxWidth(650)
		if a.extension.Configured {
			if !a.extension.Enabled {
				if ui.PrimaryButton(c, "Approve & enable").Disabled(a.extensionBusy).Clicked() {
					a.setLarkEnabled(true)
				}
			} else if ui.Button(c, "Disable").Disabled(a.extensionBusy).Clicked() {
				a.setLarkEnabled(false)
			}
		}

		ui.Text(c, "4. Personal authorization").FontWeight(600).Padding(6, 0, 0, 0)
		ui.Text(c, "Application: "+larkApplicationStatus(a.extension)).FontSize(11).TextColor(theme.TextMuted)
		ui.Text(c, "User authorization: "+larkAuthorizationStatus(a.extension.Authentication)).FontSize(11).TextColor(theme.TextMuted)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.PrimaryButton(c, "Authorize Lark").Disabled(a.extensionBusy || !a.extension.Enabled).Clicked() {
				a.authorizeLark("lark")
			}
			if ui.Button(c, "Authorize Project").Disabled(a.extensionBusy || !a.extension.Enabled || strings.TrimSpace(a.extensionEdit.ProjectCLI) == "").Clicked() {
				a.authorizeLark("project")
			}
			if ui.Button(c, "Test connection").Disabled(a.extensionBusy || !a.extension.Enabled).Clicked() {
				a.testLark()
			}
			if a.extensionBusy && a.extensionCancel != nil {
				if ui.Button(c, "Cancel authorization").Clicked() {
					a.extensionCancel()
				}
			}
		})
		if a.extensionBusy && a.extensionAuthURL != "" {
			if ui.Button(c, "Open authorization page again").Clicked() {
				_ = mygo.Shell.OpenExternal(a.extensionAuthURL)
			}
		}

		a.extensionStatus(c)
		ui.Row(c).Gap(8).Padding(8, 0, 0, 0).Children(func() {
			resetLabel := "Reset local login"
			if a.extensionConfirm == "reset" {
				resetLabel = "Confirm reset"
			}
			if ui.Button(c, resetLabel).Disabled(a.extensionBusy || !a.extension.Configured).Clicked() {
				a.resetLark()
			}
			uninstallLabel := "Uninstall"
			if a.extensionConfirm == "uninstall" {
				uninstallLabel = "Confirm uninstall"
			}
			if ui.Button(c, uninstallLabel).Disabled(a.extensionBusy).Clicked() {
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

func extensionVersionWarning(guiVersion, helperVersion string) string {
	guiVersion = extensionPackageVersion(guiVersion)
	helperVersion = extensionPackageVersion(helperVersion)
	if guiVersion == "dev" || helperVersion == "dev" || guiVersion == helperVersion {
		return ""
	}
	return "GUI " + guiVersion + " and helper " + helperVersion + " differ. Install the matching helper before diagnosing authorization."
}

func extensionPathField(c *ui.Context, label string, value *string, choose func()) {
	ui.Field(c, label, func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.TextInput(c, value).Grow(1).Placeholder("Choose an absolute native executable")
			if ui.Button(c, "Choose…").Clicked() {
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
