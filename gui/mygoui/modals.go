package mygoui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

// modelModal is the searchable model picker. Selecting a row switches the live
// model through appsession, not by typing into a terminal.
func (a *App) modelModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.modelOpen, func() {
		ui.Column(c).Width(560).MaxHeight(520).Clip().Gap(0).Children(func() {
			ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, "Model").FontSize(13).Bold()
				ui.Text(c, a.status.Provider).FontSize(11).TextColor(t.TextMuted)
				ui.Spacer(c)
				if ui.Button(c, "Refresh").Clicked() {
					go a.refreshModels()
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
			ui.Scroll(c).MaxHeight(380).Children(func() {
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
					if shown == 0 {
						ui.Text(c, "No model matches.").FontSize(12).TextColor(t.TextMuted).Padding(10)
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

// thinkingModal switches the reasoning effort for the next turn.
func (a *App) thinkingModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.thinkOpen, func() {
		ui.Column(c).Width(420).Gap(6).Children(func() {
			ui.Text(c, "Reasoning effort").FontSize(16).Bold()
			ui.Text(c, "Applies to the next turn; some models clamp to the levels they advertise.").
				FontSize(12).TextColor(t.TextMuted)
			for _, level := range appsession.ThinkingLevels() {
				level := level
				row := ui.Row(c.Key(level)).Padding(8, 10).Gap(8).Radius(6).Cursor(ui.CursorPointer)
				if level == thinkingLabel(a.status.Thinking) {
					row.Background(t.SurfacePressed)
				} else if row.Hovered() {
					row.Background(t.SurfaceHover)
				}
				row.Children(func() {
					ui.Text(c, level).FontSize(13).Grow(1)
					if level == thinkingLabel(a.status.Thinking) {
						ui.Text(c, "current").FontSize(11).TextColor(t.Accent)
					}
				})
				if row.Clicked() && level != thinkingLabel(a.status.Thinking) {
					a.setThinking(level)
				}
			}
			ui.Text(c, fmt.Sprintf("Model in use: %s", a.status.Model)).FontSize(11).TextColor(t.TextMuted)
		})
	})
}
