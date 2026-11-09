package mygoui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

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
		if len(a.status.QueuedMessages) > 0 {
			ui.Column(c).Padding(6, 12).Gap(3).Radius(10).Background(t.Surface).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("Queued messages (%d)", len(a.status.QueuedMessages))).FontSize(11).FontWeight(600).TextColor(t.TextMuted).Grow(1)
					if !a.status.Running && subtleButton(c, "Send queued").Clicked() {
						a.resumeQueued()
					}
				})
				ui.Text(c, "Sent after the current tool batch or response; pending input is held in memory.").FontSize(11).TextColor(t.TextMuted).Wrap()
				ui.Scroll(c).MaxHeight(180).Children(func() {
					ui.Column(c).Gap(3).Children(func() {
						for _, message := range a.status.QueuedDetails {
							message := message
							ui.Column(c.Key(message.ID)).Gap(3).Children(func() {
								ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
									ui.Text(c, "↳ "+strings.ReplaceAll(message.Display, "\n", " ")).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Grow(1)
									if subtleButton(c, "Edit").Label("Edit queued " + message.ID).Clicked() {
										a.queueEditID, a.queueEditText = message.ID, message.Text
									}
									if subtleButton(c, "×").Label("Remove queued " + message.ID).Clicked() {
										if err := a.ctrl.RemoveQueued(message.ID); err != nil {
											a.banner, a.bannerErr = err.Error(), true
										} else {
											a.status = a.ctrl.StatusSnapshot()
											if a.queueEditID == message.ID {
												a.queueEditID = ""
											}
										}
									}
								})
								if a.queueEditID == message.ID {
									ui.TextArea(c, &a.queueEditText).Lines(1, 4).Label("Edit queued message")
									ui.Row(c).Gap(5).Children(func() {
										if subtleButton(c, "Save queued edit").Clicked() {
											if err := a.ctrl.EditQueued(message.ID, a.queueEditText); err != nil {
												a.banner, a.bannerErr = err.Error(), true
											} else {
												a.status = a.ctrl.StatusSnapshot()
												a.queueEditID, a.queueEditText = "", ""
											}
										}
										if subtleButton(c, "Cancel queued edit").Clicked() {
											a.queueEditID, a.queueEditText = "", ""
										}
									})
								}
							})
						}
					})
				})
			})
		}
		ui.Column(c).MinHeight(84).Radius(18).Background(t.Surface).Border(1, t.Border).Clip().Children(func() {
			placeholder := "Message Golder — Enter sends, Shift+Enter adds a line"
			if a.status.ReadOnly {
				placeholder = "Read-only session — start a new chat to send messages"
			}
			input := ui.TextAreaBase(c, &a.draft).Lines(1, 8).Grow(1).
				Padding(12, 14, 8, 14).
				Placeholder(placeholder).Label("Message input").Disabled(a.status.ReadOnly)
			input.OnPaste(func(string) bool { return a.pasteClipboardImage() })
			if a.historyCaretPending {
				if a.draft == a.historyCaretText {
					end := utf8.RuneCountInString(a.draft)
					input.SetTextSelection(end, end)
				}
				a.historyCaretPending = false
			}
			_, caret := input.TextAnchorCaret()
			frameText := a.draft
			input.OnTextKey(func(k ui.TextKey) bool {
				if k.Mods == 0 && (k.Key == ui.KeyUp || k.Key == ui.KeyDown) {
					text := k.Text()
					// TextKey does not expose the live caret; the selection read at
					// the last frame is safe only while the multiline text matches.
					up := k.Key == ui.KeyUp
					if strings.ContainsRune(text, '\n') && (text != frameText || !historyEdge(text, caret, up)) {
						return false
					}
					var recalled string
					var ok bool
					if up {
						a.history.load(a.ctrl)
						recalled, ok = a.history.previous(&a.historyNav, text)
					} else {
						recalled, ok = a.history.next(&a.historyNav)
					}
					if ok {
						k.SetText(recalled)
						a.historyCaretText, a.historyCaretPending = recalled, true
						return true
					}
				}
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					text := strings.TrimSpace(k.Text())
					if text == "" && len(a.attachments) == 0 {
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
				ui.Scroll(c).MaxHeight(110).Children(func() {
					ui.Column(c).Padding(0, 10, 5, 10).Gap(5).Children(func() {
						for i := 0; i < len(a.attachments); i++ {
							i := i
							attachment := a.attachments[i]
							label, tooltip := attachment.Name, attachment.Name
							if attachment.ImagePNG == nil {
								label, tooltip = filepath.Base(attachment.Path), attachment.Path
							}
							ui.Row(c.Key(fmt.Sprintf("attachment-%d-%s", i, tooltip))).Padding(3, 7).Gap(5).Radius(7).Background(t.SurfacePressed).
								AlignItems(ui.Center).Children(func() {
								ui.Text(c, label).FontSize(11).MaxWidth(150).SingleLine().Ellipsis("…").Tooltip(tooltip)
								if subtleButton(c, "×").Label("Remove " + label).Tooltip("Remove attachment").Clicked() {
									a.attachments = slices.Delete(a.attachments, i, i+1)
								}
							})
						}
					})
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
				if a.status.Running && ready {
					queue := subtleButton(c, "↑").Size(34, 34).Radius(17).Label("Queue message").Tooltip("Queue message for the current run")
					if queue.Clicked() && a.submit(strings.TrimSpace(a.draft)) {
						a.draft = ""
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

// Paste is intercepted only for images. Text and empty clipboards continue
// through MyGo's normal editor path, including its selection and undo handling.
func (a *App) pasteClipboardImage() bool {
	if a.status.ReadOnly {
		return false
	}
	read := a.clipboardImage
	if read == nil {
		read = mygo.Clipboard.ReadImage
	}
	data := read()
	if len(data) == 0 {
		return false
	}
	name := fmt.Sprintf("Pasted image %d.png", a.pastedImageSequence+1)
	image, err := appsession.NewClipboardImage(name, data)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return true
	}
	a.pastedImageSequence++
	a.attachments = append(a.attachments, image)
	a.banner, a.bannerErr = "", false
	return true
}

func (a *App) pickAttachments() {
	win, complete, ok := a.beginFileDialog()
	if !ok {
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Attach files", ButtonLabel: "Attach", Multiple: true,
		})
		complete(func() {
			if err != nil {
				a.banner, a.bannerErr = err.Error(), true
				return
			}
			seen := make(map[string]bool, len(a.attachments)+len(paths))
			for _, attachment := range a.attachments {
				seen[attachment.Path] = true
			}
			for _, path := range paths {
				if !seen[path] {
					a.attachments = append(a.attachments, appsession.UserAttachment{Path: path})
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
