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
		input := ui.TextArea(c, &a.draft).Lines(1, 8).Grow(1)
		input.Placeholder("Message Golder — Enter sends, Shift+Enter adds a line")
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
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.status.Running {
				if ui.Button(c, "Stop").Clicked() {
					a.ctrl.Cancel()
				}
			} else if ui.PrimaryButton(c, "Send").Disabled(strings.TrimSpace(a.draft) == "").Clicked() {
				text := strings.TrimSpace(a.draft)
				a.draft = ""
				a.submit(text)
			}
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%d%% context · %s / %s",
				a.contextPercent(), formatTokens(a.status.ContextUsed), formatTokens(a.status.ContextLimit))).
				FontSize(11).TextColor(t.TextMuted)
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
			ui.Text(c, a.status.Provider).FontSize(11).TextColor(t.TextMuted)
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
