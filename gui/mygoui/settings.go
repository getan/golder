package mygoui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func (a *App) openSettings() {
	a.settingsOpen = true
	a.settingsErr, a.settingsNote = "", ""
	a.settingsEdit.NewCredential = ""
	a.settingsEdit.RemoveCredential = false
	a.refreshComputerPermissions()
	a.refreshTTYSessions()
	a.loadExtensionSettings()
	settings, err := a.ctrl.LoadProviderSettings()
	if err != nil {
		a.settingsErr = err.Error()
		return
	}
	a.installSettings(settings)
}

func (a *App) installSettings(settings appsession.ProviderSettings) {
	a.settings = settings
	a.settingsEdit = appsession.ProviderSettingsEdit{
		Provider:       settings.Provider,
		Model:          settings.Model,
		BaseURL:        settings.BaseURL,
		Protocol:       settings.Protocol,
		CredentialName: settings.Credential.Name,
		ThinkingLevel:  settings.ThinkingLevel,
	}
}

func (a *App) saveSettings(apply bool) {
	if a.settingsBusy {
		return
	}
	a.settingsBusy = true
	a.settingsErr, a.settingsNote = "", ""
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	settings, err := a.ctrl.SaveProviderSettings(ctx, a.settingsEdit, apply)
	a.settingsBusy = false
	if err != nil {
		a.settingsErr = err.Error()
		return
	}
	a.installSettings(settings)
	if apply {
		a.settingsNote = "Saved and applied to this session."
		a.applySnapshot(a.ctrl.Snapshot())
		a.startModelRefresh()
	} else {
		a.settingsNote = "Saved as the default for new sessions."
	}
}

func (a *App) testSettings() {
	if a.settingsBusy {
		return
	}
	a.settingsBusy = true
	a.settingsErr = ""
	a.settingsNote = "Testing connection…"
	edit := a.settingsEdit
	win, ctrl := a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := ctrl.TestProviderSettings(ctx, edit)
		if win == nil {
			return
		}
		win.Update(func() {
			a.settingsBusy = false
			if a.ctrl != ctrl {
				return
			}
			if err != nil {
				a.settingsErr = err.Error()
				a.settingsNote = ""
				return
			}
			a.settingsErr = ""
			a.settingsNote = "Connection succeeded."
		})
	}()
}

func (a *App) settingsModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.settingsOpen, func() {
		ui.Column(c).Width(760).MaxWidth(760).MaxHeight(680).Gap(0).Clip().Children(func() {
			ui.Row(c).Padding(16, 20).AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, "Settings").FontSize(18).Bold()
				ui.Spacer(c)
				ui.Text(c, "Provider defaults are shared with the Golder CLI").FontSize(11).TextColor(t.TextMuted)
			})
			ui.Tabs(c, &a.settingsTab, "Providers", "Permissions", "Extensions", "Display", "Terminal", "About").
				Padding(10, 16, 0, 16)
			ui.Scroll(c).Grow(1).MaxHeight(530).Children(func() {
				switch a.settingsTab {
				case 0:
					a.providerSettingsView(c)
				case 1:
					a.permissionSettingsView(c)
				case 2:
					a.extensionsSettingsView(c)
				case 3:
					a.displaySettingsView(c)
				case 4:
					a.terminalSettingsView(c)
				default:
					a.aboutSettingsView(c)
				}
			})
		})
	})
}

func (a *App) providerSettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20, 20, 20).Gap(14).Children(func() {
		ui.Text(c, "Provider defaults").FontSize(16).FontWeight(600)
		ui.Text(c, "These values are stored in config.toml and used by both the GUI and CLI. The API key is stored separately as a named credential.").
			FontSize(12).TextColor(t.TextMuted).MaxWidth(650)

		ui.Field(c, "Provider", func() {
			selectProvider := ui.Select(c, &a.settingsEdit.Provider, a.settings.Providers).FillWidth()
			if selectProvider.Changed() {
				a.settingsEdit.Model = appsession.ModelForProvider(a.settingsEdit.Provider)
				a.settingsEdit.Protocol = ""
				a.settingsEdit.BaseURL = ""
			}
		})
		ui.Field(c, "Default model", func() {
			ui.TextInput(c, &a.settingsEdit.Model).FillWidth().Placeholder("Provider default")
		})
		ui.Field(c, "Base URL", func() {
			ui.TextInput(c, &a.settingsEdit.BaseURL).FillWidth().Placeholder("Provider default endpoint")
		})
		ui.Field(c, "Protocol", func() {
			options := []string{"", "openai", "openai/resp_api", "anthropic", "gemini"}
			ui.Select(c, &a.settingsEdit.Protocol, options).FillWidth()
		})
		ui.Field(c, "Default reasoning", func() {
			ui.Select(c, &a.settingsEdit.ThinkingLevel, appsession.ThinkingLevels()).FillWidth()
		})

		ui.Column(c).Padding(12).Gap(8).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Credential").FontWeight(600)
				ui.Spacer(c)
				status, color := credentialLabel(a.settings.Credential), t.TextMuted
				if a.settings.Credential.Available {
					color = t.Success
				}
				ui.Text(c, status).FontSize(11).TextColor(color)
			})
			ui.Text(c, "AES-256-GCM encrypted on disk · automatically unlocked.").FontSize(11).TextColor(t.TextMuted)
			ui.Field(c, "Reference name", func() {
				ui.TextInput(c, &a.settingsEdit.CredentialName).FillWidth().Placeholder(a.settingsEdit.Provider + "-default")
			})
			ui.Field(c, "New API key", func() {
				ui.TextInput(c, &a.settingsEdit.NewCredential).Password().FillWidth().Placeholder("Leave blank to keep the stored value")
			})
			if a.settingsEdit.RemoveCredential {
				ui.Text(c, "The named credential will be removed when you save.").FontSize(11).TextColor(t.Warning)
			}
			ui.Row(c).Children(func() {
				if ui.Button(c, "Remove credential").Disabled(strings.TrimSpace(a.settingsEdit.CredentialName) == "").Clicked() {
					a.settingsEdit.RemoveCredential = true
					a.settingsEdit.NewCredential = ""
				}
			})
		})

		if a.settingsErr != "" {
			ui.Text(c, a.settingsErr).FontSize(12).TextColor(t.Danger)
		} else if a.settingsNote != "" {
			ui.Text(c, a.settingsNote).FontSize(12).TextColor(t.Success)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "Test connection").Disabled(a.settingsBusy).Clicked() {
				a.testSettings()
			}
			ui.Spacer(c)
			if ui.Button(c, "Save as default").Disabled(a.settingsBusy).Clicked() {
				a.saveSettings(false)
			}
			if ui.PrimaryButton(c, "Save & apply").Disabled(a.settingsBusy || a.status.Running).Tooltip("Apply to the current session when it is idle").Clicked() {
				a.saveSettings(true)
			}
		})
	})
}

func (a *App) permissionSettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20).Gap(12).Children(func() {
		ui.Text(c, "Permissions & sandbox").FontSize(16).FontWeight(600)
		ui.Text(c, "Change the session's approval mode using the hand button in the composer.").
			FontSize(12).TextColor(t.TextMuted).MaxWidth(620)
		ui.Text(c, "Readable and writable roots remain managed by the existing /boundary and grants store; the GUI editor for those rows is a later settings section.").
			FontSize(12).TextColor(t.TextMuted)
		if runtime.GOOS == "darwin" {
			ui.Text(c, "macOS Computer Use permissions").FontSize(14).FontWeight(600)
			a.macComputerPermissionsView(c, false)
		}
	})
}

func (a *App) terminalSettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20).Gap(10).Children(func() {
		ui.Text(c, "Terminal").FontSize(16).FontWeight(600)
		ui.Text(c, "MyGo terminal is an optional PTY surface for interactive commands and user shells. It is not used for the conversation, browser or tuios workspace.").
			FontSize(12).TextColor(t.TextMuted).MaxWidth(620)
		if ui.PrimaryButton(c, "Open user terminal").Clicked() {
			a.openUserTerminal()
		}
		ui.Text(c, "User terminals are explicit user processes. They are not Agent tool calls and do not inherit an Agent approval result.").
			FontSize(11).TextColor(t.Warning).MaxWidth(620)
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Agent TTY sessions").FontWeight(600)
			ui.Spacer(c)
			if ui.Button(c, "Refresh").Clicked() {
				a.refreshTTYSessions()
			}
		})
		if len(a.ttySessions) == 0 {
			ui.Text(c, "No bash session with tty=true is available.").FontSize(12).TextColor(t.TextMuted)
		}
		for _, session := range a.ttySessions {
			session := session
			ui.Row(c.Key(session.ID)).Gap(8).Padding(8, 10).Radius(8).Background(t.Surface).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					state := "exited"
					if session.Running {
						state = "running"
					}
					ui.Text(c, session.ID+" · "+state).FontSize(12).FontWeight(600)
					ui.Text(c, oneLine(session.Command, 100)).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				})
				if ui.Button(c, "Open").Clicked() {
					a.openTTYTerminal(session.ID)
				}
			})
		}
	})
}

func (a *App) aboutSettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20).Gap(10).Children(func() {
		ui.Text(c, "Golder GUI").FontSize(18).FontWeight(600)
		ui.Text(c, "A native semantic front-end over the same Golder runtime, sessions and permission gates as the CLI.").
			FontSize(12).TextColor(t.TextMuted).MaxWidth(620)
		ui.Text(c, "Version "+a.ctrl.Version()).FontSize(12)
		ui.Text(c, fmt.Sprintf("Current session: %s / %s", a.status.Provider, a.status.Model)).FontSize(12)
	})
}

func credentialLabel(v appsession.CredentialView) string {
	switch v.Source {
	case "named":
		return "Configured · " + v.Name
	case "environment":
		return "Inherited · " + v.EnvVar
	case "explicit":
		return "Configured for this launch"
	default:
		return "Not configured"
	}
}
