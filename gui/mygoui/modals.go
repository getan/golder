package mygoui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

// modelModal combines provider, model and reasoning behind one composer control.
func (a *App) modelModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.modelOpen, func() {
		ui.Column(c).Width(580).MaxHeight(620).Clip().Gap(0).Children(func() {
			ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, "Provider, model & reasoning").FontSize(13).Bold()
				ui.Spacer(c)
				if subtleButton(c, "Refresh").Clicked() {
					if a.ctrl != nil {
						a.providers = a.ctrl.Providers()
					}
					a.startModelRefresh()
				}
				if subtleButton(c, "Close").Clicked() {
					a.modelOpen = false
				}
			})
			ui.Row(c).Padding(8, 12, 0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Provider").FontSize(12).FontWeight(600)
				if a.providerSelection == "" {
					a.providerSelection = a.status.Provider
				}
				selection := ui.Select(c, &a.providerSelection, a.providers).Grow(1).Disabled(a.status.Running).
					Tooltip("Configured providers; manage credentials in Settings")
				if selection.Changed() && a.providerSelection != a.status.Provider {
					a.setProvider(a.providerSelection)
				}
			})
			ui.Row(c).Padding(8, 12).Children(func() {
				ui.TextInput(c, &a.modelQuery).Grow(1).AutoFocus().Placeholder("Filter models…")
			})
			if a.modelsErr != "" {
				ui.Text(c, a.modelsErr).FontSize(11.5).TextColor(t.Danger).Padding(6, 16)
			}
			if !a.modelsReady {
				ui.Text(c, "Loading the provider's catalog…").FontSize(12).TextColor(t.TextMuted).Padding(8, 16)
			}
			q := strings.ToLower(strings.TrimSpace(a.modelQuery))
			ui.Scroll(c).MaxHeight(300).Children(func() {
				ui.Column(c).Padding(6).Gap(1).Children(func() {
					shown := 0
					for _, m := range a.models {
						if q != "" && !strings.Contains(strings.ToLower(m.ID), q) {
							continue
						}
						shown++
						row := ui.Row(c.Key(m.ID)).Padding(6, 10).Gap(8).Radius(6).Cursor(ui.CursorPointer)
						if m.Current {
							row.Background(t.SurfacePressed)
						} else if row.Hovered() {
							row.Background(t.SurfaceHover)
						}
						row.Children(func() {
							ui.Text(c, m.ID).FontSize(13).Grow(1).SingleLine().Ellipsis("…")
							if m.Default {
								ui.Text(c, "default").FontSize(10.5).TextColor(t.TextMuted)
							}
							if m.Current {
								ui.Text(c, "current").FontSize(10.5).TextColor(t.Accent)
							}
						})
						if row.Clicked() && !m.Current {
							a.setModel(m.Provider, m.ID)
						}
					}
					if shown == 0 && a.modelsReady {
						ui.Text(c, "No model matches.").FontSize(12).TextColor(t.TextMuted).Padding(10)
					}
				})
			})
			ui.Column(c).Padding(10, 14, 14, 14).Gap(8).
				BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, "Reasoning effort").FontSize(12).FontWeight(600)
				ui.Text(c, "Applies to the next turn; unsupported levels are clamped by the provider.").
					FontSize(11).TextColor(t.TextMuted)
				ui.Row(c).Gap(5).Children(func() {
					current := thinkingLabel(a.status.Thinking)
					for _, level := range appsession.ThinkingLevels() {
						level := level
						button := ui.ButtonBase(c.Key("reasoning:"+level)).Padding(5, 8).Radius(6).Label(level)
						if level == current {
							button.Background(t.SurfacePressed)
						} else if button.Hovered() {
							button.Background(t.SurfaceHover)
						}
						button.Children(func() {
							ui.Text(c, level).FontSize(11.5)
						})
						if button.Clicked() && level != current {
							a.setThinking(level)
						}
					}
				})
			})
		})
	})
}

// permissionModal switches the approval mode.
func (a *App) permissionModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.permOpen, func() {
		ui.Column(c).Width(480).Gap(8).Children(func() {
			ui.Text(c, "Approval mode").FontSize(16).Bold()
			ui.Text(c, "The mode applies to the next tool call, exactly as `/permissions` does in the CLI.").
				FontSize(12).TextColor(t.TextMuted)
			for _, mode := range appsession.PermissionModes() {
				mode := mode
				row := ui.Row(c.Key(mode)).Padding(9, 10).Gap(8).Radius(6).Cursor(ui.CursorPointer)
				if mode == a.status.Permission {
					row.Background(t.SurfacePressed)
				} else if row.Hovered() {
					row.Background(t.SurfaceHover)
				}
				row.Children(func() {
					ui.Column(c).Grow(1).Gap(1).Children(func() {
						ui.Text(c, mode).FontWeight(600).FontSize(13)
						ui.Text(c, permissionHint(mode)).FontSize(11.5).TextColor(t.TextMuted)
					})
					if mode == a.status.Permission {
						ui.Text(c, "current").FontSize(11).TextColor(t.Accent)
					}
				})
				if row.Clicked() && mode != a.status.Permission {
					a.setPermission(mode)
				}
			}
		})
	})
}

func permissionHint(mode string) string {
	switch mode {
	case "read-only":
		return "Inspect only; writes, network and sandbox escapes are blocked."
	case "ask":
		return "The reviewer filters routine calls; the rest asks you once per capability."
	case "full-access":
		return "No review, prompts or sandbox. Only the hard blocklist applies."
	default:
		return "Routine calls run; anything the reviewer cannot clear asks you."
	}
}
