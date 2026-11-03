package mygoui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/internal/agentcore"
)

// composer is the message field and the live configuration footer.
func (a *App) composer(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(10, 24, 14, 24).Gap(8).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).MinHeight(68).Radius(18).Background(t.Surface).Border(1, t.Border).Clip().Children(func() {
			input := ui.TextAreaBase(c, &a.draft).Lines(1, 8).Grow(1).
				Padding(12, 54, 40, 14).
				Placeholder("Message Golder — Enter sends, Shift+Enter adds a line")
			input.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					text := strings.TrimSpace(k.Text())
					if text == "" {
						return true
					}
					if a.status.Running {
						a.banner, a.bannerErr = "A turn is already running; stop it first.", true
						return true
					}
					a.submit(text)
					k.SetText("")
					return true
				}
				return false
			})

			ready := strings.TrimSpace(a.draft) != ""
			label, accessible := "↑", "Send message"
			face, hover, pressed := t.Accent, t.AccentHover, t.AccentPressed
			if a.status.Running {
				label, accessible = "■", "Stop generation"
				face, hover, pressed = t.Danger, t.Danger, t.Danger
			} else if !ready {
				face, hover, pressed = t.SurfacePressed, t.SurfacePressed, t.SurfacePressed
			}
			send := ui.ButtonBase(c).Absolute().Right(10).Bottom(9).Size(34, 34).Radius(17).
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
					a.draft = ""
					a.submit(text)
				}
			}
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "Model · "+a.status.Model).Tooltip("Switch model").Clicked() {
				a.modelOpen = true
				if !a.modelsReady {
					go a.refreshModels()
				}
			}
			if ui.Button(c, "Think · "+thinkingLabel(a.status.Thinking)).Tooltip("Reasoning level").Clicked() {
				a.thinkOpen = true
			}
			if ui.Button(c, "Mode · "+a.status.Permission).Tooltip("Approval mode").Clicked() {
				a.permOpen = true
			}
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%d%% context · %s / %s",
				a.contextPercent(), formatTokens(a.status.ContextUsed), formatTokens(a.status.ContextLimit))).
				FontSize(11).TextColor(t.TextMuted)
		})
	})
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
