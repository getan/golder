package mygoui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/internal/computeruse"
)

const computerPanelWidth = 600

func (a *App) toggleComputerPanel() {
	if a.computerSession != nil && a.computerSession.Status().Active {
		if a.computerOpen {
			a.hideComputerPanel()
		} else {
			a.showComputerPanel()
		}
		return
	}
	if a.computerSession != nil {
		a.stopComputer()
	}
	a.computerPickerOpen = true
	a.loadComputerTargets()
}

func (a *App) loadComputerTargets() {
	if a.computerBusy || a.win == nil {
		return
	}
	if a.computerBackend != nil {
		old := a.computerBackend
		a.computerBackend = nil
		go old.Close()
	}
	a.computerBusy, a.computerError = true, ""
	a.computerTargets, a.computerSelected = nil, -1
	a.clearComputerCandidate()
	a.computerPreviewFailed, a.computerSkipPreview = false, false
	a.computerSetupSeq++
	seq, win := a.computerSetupSeq, a.win
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a.computerSetupStop = cancel
	go func() {
		backend, err := computeruse.NewPlatformBackend()
		var targets []computeruse.Target
		if err == nil {
			targets, err = backend.Targets(ctx)
		}
		permissions := computeruse.ProbePermissions()
		win.Update(func() {
			defer cancel()
			if seq != a.computerSetupSeq || !a.computerPickerOpen {
				if backend != nil {
					go backend.Close()
				}
				return
			}
			a.computerBusy, a.computerSetupStop = false, nil
			a.computerPermission = permissions
			if ctx.Err() != nil {
				a.computerError = ctx.Err().Error()
				if backend != nil {
					go backend.Close()
				}
				return
			}
			if err != nil {
				a.computerError = err.Error()
				if backend != nil {
					go backend.Close()
				}
				return
			}
			targets, a.computerSelected = matchComputerTargets(a.computerSuggested, targets)
			a.computerBackend, a.computerTargets = backend, targets
		})
	}()
}

func matchComputerTargets(suggested string, targets []computeruse.Target) ([]computeruse.Target, int) {
	matches := computeruse.MatchApplications(suggested, targets)
	if len(matches) == 1 {
		return targets, matches[0]
	}
	if suggested != "" && len(matches) > 1 {
		filtered := make([]computeruse.Target, 0, len(matches))
		for _, index := range matches {
			filtered = append(filtered, targets[index])
		}
		return filtered, -1
	}
	return targets, -1
}

func (a *App) startComputer(control bool) {
	if a.computerBusy || a.computerBackend == nil || a.ctrl == nil || a.win == nil || a.computerSelected < 0 || a.computerSelected >= len(a.computerTargets) {
		return
	}
	if control && a.status.Permission == "read-only" {
		a.computerError = "Control is unavailable while the session is read-only."
		return
	}
	target := a.computerTargets[a.computerSelected]
	gate, requiresPreview := a.computerBackend.(computeruse.OperatorPreviewBackend)
	if requiresPreview && !a.computerSkipPreview {
		if a.computerCandidateID == "" {
			a.prepareComputerCandidate(gate, target, control)
			return
		}
		if a.computerCandidateTarget != target.ID || a.computerCandidateControl != control || a.computerCandidateSession != a.status.SessionID {
			a.computerError = "The preview no longer matches the selected window or grant."
			return
		}
	}
	a.computerBusy, a.computerError = true, ""
	a.computerSetupSeq++
	seq, win, backend := a.computerSetupSeq, a.win, a.computerBackend
	conversation, candidateID := a.status.SessionID, a.computerCandidateID
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a.computerSetupStop = cancel
	go func() {
		session := computeruse.New(backend)
		var err error
		if requiresPreview && candidateID != "" {
			err = gate.ConfirmOperatorPreview(ctx, target, candidateID)
		}
		if err == nil {
			err = session.Start(ctx, target, computeruse.Capabilities{Observe: true, Control: control})
		}
		attached := false
		if err == nil {
			if err = ctx.Err(); err == nil {
				err = a.ctrl.AttachComputer(conversation, session)
				attached = err == nil
			}
		}
		win.Update(func() {
			defer cancel()
			if seq != a.computerSetupSeq || !a.computerPickerOpen {
				if attached {
					a.ctrl.DetachComputer(session)
				}
				session.Stop()
				return
			}
			a.computerBusy, a.computerSetupStop = false, nil
			if ctx.Err() != nil {
				if attached {
					a.ctrl.DetachComputer(session)
				}
				session.Stop()
				a.clearComputerCandidate()
				a.computerError = ctx.Err().Error()
				return
			}
			if err != nil {
				a.computerError = err.Error()
				a.clearComputerCandidate()
				session.Stop()
				return
			}
			requested := a.computerSuggested != ""
			a.computerSuggested = ""
			a.computerRequestPicker = false
			a.ctrl.ResetComputerRequest()
			a.computerBackend = nil // ownership transferred to session
			a.computerSession, a.computerPickerOpen = session, false
			a.clearComputerCandidate()
			a.showComputerPanel()
			if requested {
				go func() {
					if err := a.ctrl.ContinueComputer(context.Background(), conversation, session); err != nil {
						win.Update(func() {
							if a.computerSession == session {
								a.banner, a.bannerErr = "Computer Use continuation: "+err.Error(), true
							}
						})
					}
				}()
			}
		})
	}()
}

func (a *App) clearComputerCandidate() {
	a.computerCandidate, a.computerCandidateID, a.computerCandidateTarget, a.computerCandidateSession = nil, "", "", ""
	a.computerCandidateControl = false
}

func (a *App) prepareComputerCandidate(gate computeruse.OperatorPreviewBackend, target computeruse.Target, control bool) {
	a.computerBusy, a.computerError, a.computerPreviewFailed = true, "", false
	a.computerSetupSeq++
	seq, win, conversation := a.computerSetupSeq, a.win, a.status.SessionID
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	a.computerSetupStop = cancel
	go func() {
		preview, err := gate.PrepareOperatorPreview(ctx, target)
		var bitmap *ui.Bitmap
		if err == nil {
			config, format, decodeErr := image.DecodeConfig(bytes.NewReader(preview.PNG))
			if preview.ID == "" || len(preview.PNG) == 0 || len(preview.PNG) > computeruse.MaxFrameBytes || decodeErr != nil || format != "png" || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > computeruse.MaxFramePixels {
				err = errors.New("the local window preview is invalid or too large")
			} else if decoded, _, decodeErr := image.Decode(bytes.NewReader(preview.PNG)); decodeErr != nil {
				err = decodeErr
			} else {
				bitmap = ui.NewBitmap(decoded)
			}
		}
		win.Update(func() {
			defer cancel()
			if seq != a.computerSetupSeq || !a.computerPickerOpen {
				return
			}
			a.computerBusy, a.computerSetupStop = false, nil
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if a.status.SessionID != conversation {
				err = errors.New("conversation changed during window selection")
			}
			if err != nil {
				a.computerError, a.computerPreviewFailed = err.Error(), true
				return
			}
			a.computerCandidate, a.computerCandidateID = bitmap, preview.ID
			a.computerCandidateTarget, a.computerCandidateControl = target.ID, control
			a.computerCandidateSession = conversation
		})
	}()
}

func (a *App) showComputerPanel() {
	a.filesOpen, a.changesOpen = false, false
	a.closeBrowserPanel()
	a.computerOpen = true
	a.startComputerPreview()
}

func (a *App) hideComputerPanel() {
	if a.computerOpen && a.computerSession != nil {
		status := a.computerSession.Status()
		if status.Active && !status.Paused {
			a.computerSession.Pause()
			a.banner = "Computer Use paused because its panel was hidden. Reopen it to resume."
		}
	}
	a.computerOpen = false
	if a.computerPreviewStop != nil {
		a.computerPreviewStop()
		a.computerPreviewStop = nil
	}
}

func (a *App) stopComputer() {
	a.computerSetupSeq++
	if a.computerSetupStop != nil {
		a.computerSetupStop()
		a.computerSetupStop = nil
	}
	if a.computerSession != nil {
		a.computerSession.Stop()
	}
	a.hideComputerPanel()
	a.computerPickerOpen, a.computerBusy = false, false
	a.computerSuggested = ""
	a.computerRequestPicker = false
	if a.ctrl != nil {
		a.ctrl.ResetComputerRequest()
		a.ctrl.StopComputer()
	}
	if a.computerSession != nil {
		session := a.computerSession
		a.computerSession = nil
		go session.Close()
	}
	if a.computerBackend != nil {
		backend := a.computerBackend
		a.computerBackend = nil
		go backend.Close()
	}
	a.computerTargets, a.computerFrame = nil, nil
	a.clearComputerCandidate()
	a.computerPreviewFailed, a.computerSkipPreview = false, false
}

func (a *App) startComputerPreview() {
	if a.computerPreviewStop != nil || a.computerSession == nil || a.win == nil || !a.computerSession.SupportsCapture() {
		return
	}
	session, win := a.computerSession, a.win
	ctx, cancel := context.WithCancel(context.Background())
	a.computerPreviewStop = cancel
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if !session.Status().Active {
				return
			}
			if !session.Status().Paused {
				frameCtx, stop := context.WithTimeout(ctx, 20*time.Second)
				frame, err := session.Capture(frameCtx)
				stop()
				var bitmap *ui.Bitmap
				if err == nil {
					image, _, decodeErr := image.Decode(bytes.NewReader(frame.PNG))
					if decodeErr != nil {
						err = decodeErr
					} else {
						bitmap = ui.NewBitmap(image)
					}
				}
				win.Update(func() {
					if a.computerSession != session || !a.computerOpen {
						return
					}
					if bitmap != nil {
						a.computerFrame, a.computerError = bitmap, ""
					} else if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, computeruse.ErrPaused) {
						a.computerError = err.Error()
					}
				})
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *App) computerPicker(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.computerPickerOpen, func() {
		ui.Column(c).Width(620).MaxHeight(650).Gap(10).Children(func() {
			ui.Text(c, "Computer Use").FontSize(16).Bold()
			if a.computerCandidate != nil && a.computerCandidateID != "" {
				ui.Text(c, "Confirm the system-selected preview is the same window you selected in Golder. Until you confirm, the image is local and unavailable to the agent.").FontSize(12).TextColor(t.TextMuted).Wrap()
				if a.computerSelected >= 0 && a.computerSelected < len(a.computerTargets) {
					target := a.computerTargets[a.computerSelected]
					ui.Text(c, "Selected: "+target.Application+" · "+target.Window).FontSize(12).Bold().Wrap()
				}
				ui.Image(c, a.computerCandidate).Width(590).Height(320).Fit(ui.Contain)
				if a.computerError != "" {
					ui.Text(c, a.computerError).FontSize(11).TextColor(t.Warning).Wrap()
				}
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "Cancel").Clicked() {
						a.computerPickerOpen = false
						a.clearComputerCandidate()
					}
					ui.Spacer(c)
					if ui.PrimaryButton(c, "Confirm same window").Disabled(a.computerBusy || (a.computerCandidateControl && a.status.Permission == "read-only")).Clicked() {
						a.startComputer(a.computerCandidateControl)
					}
				})
				return
			}
			ui.Text(c, "Choose one application window. Its contents may be sent to your configured model provider when the agent requests a snapshot or screenshot. System permissions do not grant the agent authority to switch targets.").FontSize(12).TextColor(t.TextMuted).Wrap()
			if a.computerSuggested != "" {
				ui.Text(c, "Requested application: "+a.computerSuggested+". Confirm the window and permission below.").FontSize(11).TextColor(t.TextMuted).Wrap()
				if !a.computerBusy && a.computerBackend != nil && len(computeruse.MatchApplications(a.computerSuggested, a.computerTargets)) == 0 {
					ui.Text(c, "No exact match; choose another window or cancel.").FontSize(11).TextColor(t.Warning).Wrap()
				}
			}
			ui.Text(c, fmt.Sprintf("System permissions: screen capture %s · accessibility %s", a.computerPermission.ScreenCapture, a.computerPermission.Accessibility)).FontSize(11).TextColor(t.TextMuted).Wrap()
			if runtime.GOOS == "darwin" && (a.computerPermission.ScreenCapture != "granted" || a.computerPermission.Accessibility != "granted") {
				ui.Text(c, "Use these buttons to register Golder GUI in macOS Privacy & Security. After granting, quit and reopen the app if macOS asks.").FontSize(11).TextColor(t.TextMuted).Wrap()
				ui.Row(c).Gap(8).Children(func() {
					if a.computerPermission.Accessibility != "granted" && ui.Button(c, "Request Accessibility").Clicked() {
						go computeruse.RequestMacPermission("accessibility")
					}
					if a.computerPermission.ScreenCapture != "granted" && ui.Button(c, "Request Screen Recording").Clicked() {
						go computeruse.RequestMacPermission("screen")
					}
					if ui.Button(c, "Open Privacy Settings").Clicked() {
						go mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
					}
				})
			}
			if a.computerError != "" {
				ui.Text(c, a.computerError).FontSize(11).TextColor(t.Warning).Wrap()
			}
			if a.computerSkipPreview {
				ui.Text(c, "Semantic access only: no window image will be available to the agent.").FontSize(11).TextColor(t.Warning).Wrap()
			} else if a.computerPreviewFailed && ui.Button(c, "Continue without preview").Disabled(a.computerBusy).Clicked() {
				a.computerSkipPreview = true
			}
			if a.computerBusy {
				ui.Text(c, "Checking available windows…").FontSize(11).TextColor(t.TextMuted)
			}
			ui.Scroll(c).MaxHeight(340).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					for i, target := range a.computerTargets {
						i, target := i, target
						label := target.Application
						if target.Window != "" {
							label += " · " + target.Window
						}
						if target.Detail != "" {
							label += " · " + target.Detail
						}
						if ui.Button(c.Key(target.ID), label).FillWidth().Selected(a.computerSelected == i).Disabled(a.computerBusy).Clicked() {
							a.computerSelected = i
						}
					}
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Refresh windows").Disabled(a.computerBusy).Clicked() {
					a.loadComputerTargets()
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					a.computerPickerOpen = false
					a.computerSuggested = ""
					a.computerRequestPicker = false
					if a.ctrl != nil {
						a.ctrl.ResetComputerRequest()
					}
				}
				if ui.Button(c, "View only").Disabled(a.computerBusy || a.computerSelected < 0).Clicked() {
					a.startComputer(false)
				}
				if ui.PrimaryButton(c, "Allow control").Disabled(a.computerBusy || a.computerSelected < 0 || a.status.Permission == "read-only").Clicked() {
					a.startComputer(true)
				}
			})
		})
	})
}

func (a *App) computerPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(computerPanelWidth).MinWidth(360).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Height(42).Padding(6, 10).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, computerMonochromeIcon).Size(15, 15)
			ui.Text(c, "Computer Use").FontSize(12).FontWeight(600)
			if a.computerSession != nil {
				status := a.computerSession.Status()
				ui.Text(c, status.Target.Application+" · "+status.Target.Window).Grow(1).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			} else {
				ui.Spacer(c)
			}
			if ui.Button(c, "×").Tooltip("Pause and hide the Computer Use panel").Clicked() {
				a.hideComputerPanel()
			}
		})
		status := computeruse.Status{}
		if a.computerSession != nil {
			status = a.computerSession.Status()
		}
		ui.Row(c).Padding(6, 10).Gap(8).AlignItems(ui.Center).Children(func() {
			label := "Stopped"
			if status.Active && status.Paused {
				label = "Paused · take over in the target window"
			} else if status.Active && status.Capabilities.Control {
				label = "Agent control permitted · actions require confirmation"
			} else if status.Active {
				label = "Observation only"
			}
			ui.Text(c, label).FontSize(10.5).TextColor(t.TextMuted).Grow(1)
			if status.Active && !status.Paused && ui.Button(c, "Pause / take over").Clicked() {
				a.computerSession.Pause()
			}
			if status.Active && status.Paused && ui.Button(c, "Resume").Clicked() {
				a.resumeComputer()
			}
			if status.Active && ui.Button(c, "Stop").Clicked() {
				a.stopComputer()
			}
		})
		if a.computerError != "" {
			ui.Text(c, a.computerError).Padding(6, 10).FontSize(11).TextColor(t.Warning).Wrap()
		}
		ui.Box(c).Grow(1).MinHeight(0).Background(t.Surface).Children(func() {
			if a.computerFrame != nil {
				ui.Image(c, a.computerFrame).Fill().Fit(ui.Contain)
			} else {
				ui.Column(c).Fill().Center().Children(func() {
					message := "Waiting for target window preview…"
					if a.computerSession != nil && !a.computerSession.SupportsCapture() {
						message = "Window preview unavailable; semantic access only."
					}
					ui.Text(c, message).FontSize(12).TextColor(t.TextMuted)
				})
			}
		})
	})
}

func (a *App) resumeComputer() {
	if a.computerSession == nil || a.win == nil {
		return
	}
	session, win := a.computerSession, a.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := session.Resume(ctx)
		win.Update(func() {
			if a.computerSession == session && err != nil {
				a.computerError = err.Error()
			}
		})
	}()
}
