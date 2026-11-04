package mygoui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/internal/agentcore"
)

// composer keeps the project controls inside the message box, matching the
// Codex desktop layout: attachments and approvals left, model/reasoning and
// send right.
func (a *App) composer(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(10, 24, 12, 24).Gap(5).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).MinHeight(84).Radius(18).Background(t.Surface).Border(1, t.Border).Clip().Children(func() {
			input := ui.TextAreaBase(c, &a.draft).Lines(1, 8).Grow(1).
				Padding(12, 14, 8, 14).
				Placeholder("Message Golder — Enter sends, Shift+Enter adds a line")
			input.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					text := strings.TrimSpace(k.Text())
					if text == "" && len(a.attachments) == 0 {
						return true
					}
					if a.status.Running {
						a.banner, a.bannerErr = "A turn is already running; stop it first.", true
						return true
					}
					if a.submit(text) {
						k.SetText("")
					}
					return true
				}
				return false
			})

			if len(a.attachments) > 0 {
				ui.Row(c).Padding(0, 10, 5, 10).Gap(5).AlignItems(ui.Center).Children(func() {
					shown := len(a.attachments)
					if shown > 3 {
						shown = 3
					}
					for i := 0; i < shown; i++ {
						i := i
						ui.Row(c.Key(a.attachments[i])).Padding(3, 7).Gap(5).Radius(7).Background(t.SurfacePressed).
							AlignItems(ui.Center).Children(func() {
							ui.Text(c, filepath.Base(a.attachments[i])).FontSize(11).MaxWidth(150).SingleLine().Ellipsis("…").Tooltip(a.attachments[i])
							if ui.Button(c, "×").Tooltip("Remove attachment").Clicked() {
								a.attachments = append(a.attachments[:i], a.attachments[i+1:]...)
							}
						})
					}
					if len(a.attachments) > shown {
						ui.Text(c, fmt.Sprintf("+%d", len(a.attachments)-shown)).FontSize(11).TextColor(t.TextMuted)
					}
				})
			}

			ready := strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0
			label, accessible := "↑", "Send message"
			face, hover, pressed := t.Accent, t.AccentHover, t.AccentPressed
			if a.status.Running {
				label, accessible = "■", "Stop generation"
				face, hover, pressed = t.Danger, t.Danger, t.Danger
			} else if !ready {
				face, hover, pressed = t.SurfacePressed, t.SurfacePressed, t.SurfacePressed
			}
			ui.Row(c).Padding(0, 9, 9, 9).Gap(6).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "+").Tooltip("Attach files or images").Clicked() {
					a.pickAttachments()
				}
				if ui.Button(c, "✋").Tooltip("Approval mode · " + a.status.Permission).Clicked() {
					a.permOpen = true
				}
				ui.Spacer(c)
				modelLabel := a.status.Model + " · " + thinkingLabel(a.status.Thinking)
				if ui.Button(c, modelLabel).Tooltip("Model and reasoning effort").Clicked() {
					a.modelOpen = true
					if !a.modelsReady {
						go a.refreshModels()
					}
				}
				send := ui.ButtonBase(c).Size(34, 34).Radius(17).
					Label(accessible).Tooltip(accessible).Disabled(!a.status.Running && !ready).Background(face)
				if send.Hovered() {
					send.Background(hover)
				} else if send.Pressed() {
					send.Background(pressed)
				}
				send.Children(func() {
					color := t.AccentText
					if !a.status.Running && !ready {
						color = t.TextMuted
					}
					ui.Text(c, label).FontSize(17).FontWeight(700).TextColor(color).TextAlign(ui.Center)
				})
				if send.Clicked() {
					if a.status.Running {
						a.ctrl.Cancel()
					} else {
						text := strings.TrimSpace(a.draft)
						if a.submit(text) {
							a.draft = ""
						}
					}
				}
			})
		})
		ui.Row(c).Justify(ui.End).Padding(0, 4).Children(func() {
			ui.Text(c, fmt.Sprintf("%d%% context · %s / %s",
				a.contextPercent(), formatTokens(a.status.ContextUsed), formatTokens(a.status.ContextLimit))).
				FontSize(11).TextColor(t.TextMuted)
		})
	})
}

func (a *App) pickAttachments() {
	win := a.win
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Attach files", ButtonLabel: "Attach", Multiple: true,
		})
		if win == nil {
			return
		}
		win.Update(func() {
			if err != nil {
				a.banner, a.bannerErr = err.Error(), true
				return
			}
			seen := make(map[string]bool, len(a.attachments)+len(paths))
			for _, path := range a.attachments {
				seen[path] = true
			}
			for _, path := range paths {
				if !seen[path] {
					a.attachments = append(a.attachments, path)
					seen[path] = true
				}
			}
		})
	}()
}

func thinkingLabel(level agentcore.ThinkingLevel) string {
	if level == "" {
		return "off"
	}
	return string(level)
}

func formatTokens(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
}

// agentThinkingLevel converts the picker's label to the typed level.
func agentThinkingLevel(s string) agentcore.ThinkingLevel {
	switch s {
	case "minimal":
		return agentcore.ThinkingMinimal
	case "low":
		return agentcore.ThinkingLow
	case "medium":
		return agentcore.ThinkingMedium
	case "high":
		return agentcore.ThinkingHigh
	case "xhigh":
		return agentcore.ThinkingXHigh
	case "max":
		return agentcore.ThinkingMax
	default:
		return agentcore.ThinkingOff
	}
}
