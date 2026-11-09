package mygoui

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

// loadActions refreshes the command catalog from the controller.
func (a *App) loadActions() {
	a.actions = a.ctrl.ListActions(context.Background())
}

// filteredActions applies the palette query, matching the label, description
// or category.
func (a *App) filteredActions() []appsession.Action {
	q := strings.ToLower(strings.TrimSpace(a.paletteQuery))
	if q == "" {
		return a.actions
	}
	out := make([]appsession.Action, 0, len(a.actions))
	for _, act := range a.actions {
		hay := strings.ToLower(act.Label + " " + act.Description + " " + act.Category)
		if strings.Contains(hay, q) || fuzzy(hay, q) {
			out = append(out, act)
		}
	}
	return out
}

// paletteModal is the command palette: every slash command, skill and
// window-only action in one searchable list.
func (a *App) paletteModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.paletteOpen, func() {
		ui.Column(c).Width(600).MaxHeight(520).Gap(0).Clip().Children(func() {
			ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, "⌘").FontSize(12).TextColor(t.TextMuted)
				in := ui.TextInput(c, &a.paletteQuery).Grow(1).AutoFocus().
					Placeholder("Type a command, skill or action…")
				if in.Changed() {
					a.paletteSel = 0
				}
			})
			items := a.filteredActions()
			if a.paletteSel >= len(items) {
				a.paletteSel = max(len(items)-1, 0)
			}
			ui.Scroll(c).MaxHeight(430).Children(func() {
				ui.Column(c).Padding(6).Gap(1).Children(func() {
					if len(items) == 0 {
						ui.Text(c, "No matching action.").FontSize(12).TextColor(t.TextMuted).Padding(10)
					}
					for i, act := range items {
						row := ui.Row(c.Key(act.ID)).Padding(6, 10).Gap(8).Radius(6).Cursor(ui.CursorPointer)
						if i == a.paletteSel {
							row.Background(t.SurfacePressed)
						} else if row.Hovered() {
							row.Background(t.SurfaceHover)
						}
						row.Children(func() {
							ui.Column(c).Grow(1).Gap(1).Children(func() {
								ui.Text(c, act.Label).FontSize(13).FontWeight(500)
								if act.Description != "" {
									ui.Text(c, act.Description).FontSize(11.5).TextColor(t.TextMuted).
										SingleLine().Ellipsis("…")
								}
							})
							if act.ArgumentHint != "" {
								ui.Text(c, act.ArgumentHint).FontSize(11).TextColor(t.TextMuted)
							}
							ui.Text(c, act.Category).FontSize(10.5).TextColor(t.TextMuted)
						})
						if row.Clicked() {
							if act.ArgumentHint != "" && !strings.HasPrefix(act.ID, "gui:") {
								a.paletteOpen = false
								if strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0 {
									a.banner, a.bannerErr = "Send or clear the current draft before choosing a command.", true
								} else {
									a.draft = "/" + strings.TrimPrefix(act.ID, "/") + " "
								}
							} else {
								a.runAction(act.ID, "")
							}
						}
					}
				})
			})
		})
	})
}

// fuzzy reports whether the letters of q appear in s, in order.
func fuzzy(s, q string) bool {
	i := 0
	for _, r := range s {
		if i < len(q) && byte(r) == q[i] {
			i++
		}
	}
	return i == len(q)
}
