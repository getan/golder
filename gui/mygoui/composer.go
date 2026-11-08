package mygoui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
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
			placeholder := "Message Golder — Enter sends, Shift+Enter adds a line"
			if a.status.ReadOnly {
				placeholder = "Read-only session — start a new chat to send messages"
			}
			input := ui.TextAreaBase(c, &a.draft).Lines(1, 8).Grow(1).
				Padding(12, 14, 8, 14).
				Placeholder(placeholder).Disabled(a.status.ReadOnly)
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
							if subtleButton(c, "×").Tooltip("Remove attachment").Clicked() {
								a.attachments = append(a.attachments[:i], a.attachments[i+1:]...)
							}
						})
					}
					if len(a.attachments) > shown {
						ui.Text(c, fmt.Sprintf("+%d", len(a.attachments)-shown)).FontSize(11).TextColor(t.TextMuted)
					}
				})
			}

			ready := !a.status.ReadOnly && (strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0)
			label, accessible := "↑", "Send message"
			face, hover, pressed, buttonText := composerControlPalette(t, a.status.Running, ready)
			if a.status.Running {
				label, accessible = "■", "Stop generation"
			}
			ui.Row(c).Padding(0, 9, 9, 9).Gap(6).AlignItems(ui.Center).Children(func() {
				if subtleButton(c, "+", a.status.ReadOnly).Tooltip("Attach files or images").Clicked() {
					a.pickAttachments()
				}
				if compactIconButton(c, "approval-mode", approvalHandIcon, "Approval mode", "Approval mode · "+a.status.Permission).Clicked() {
					a.permOpen = true
				}
				ui.Spacer(c)
				modelLabel := a.status.Provider + " · " + a.status.Model + " · " + thinkingLabel(a.status.Thinking)
				modelButton := subtleButton(c, "").Shrink(1).MinWidth(0).MaxWidth(240).Clip().Label(modelLabel).
					Tooltip("Provider, model and reasoning effort · " + modelLabel)
				modelButton.Children(func() {
					ui.Text(c, modelLabel).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				})
				if modelButton.Clicked() {
					a.modelOpen = true
					a.providerSelection = a.status.Provider
					if a.ctrl != nil {
						a.providers = a.ctrl.Providers()
					}
					if !a.modelsReady {
						a.startModelRefresh()
					}
				}
				send := ui.ButtonBase(c).Size(34, 34).Radius(17).
					Label(accessible).Tooltip(accessible).Disabled(a.status.ReadOnly || (!a.status.Running && !ready)).Background(face)
				hoverBorder(c, send)
				if send.Hovered() {
					send.Background(hover)
				} else if send.Pressed() {
					send.Background(pressed)
				}
				send.Children(func() {
					ui.Text(c, label).FontSize(17).FontWeight(700).TextColor(buttonText).TextAlign(ui.Center)
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
		ui.Row(c).Padding(0, 4).AlignItems(ui.Center).Children(func() {
			if a.status.ReadOnly {
				ui.Text(c, readOnlySessionLabel(a.status)).FontSize(11).TextColor(t.Warning)
			}
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%d%% context · %s / %s",
				a.contextPercent(), formatTokens(a.status.ContextUsed), formatTokens(a.status.ContextLimit))).
				FontSize(11).TextColor(t.TextMuted)
		})
	})
}

func readOnlySessionLabel(status appsession.Status) string {
	label := "Read-only · session is open elsewhere"
	if status.Writer.Frontend != "" {
		label = "Read-only · active in " + status.Writer.Frontend
	}
	if status.Writer.PID > 0 {
		label += fmt.Sprintf(" (PID %d)", status.Writer.PID)
	}
	return label
}

func composerControlPalette(t *ui.Theme, running, ready bool) (face, hover, pressed, text ui.Color) {
	if !running && !ready {
		return t.Surface, t.Surface, t.Surface, t.TextMuted
	}
	return t.SurfacePressed, t.SurfaceHover, t.Border, t.Text
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
