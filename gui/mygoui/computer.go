package mygoui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"time"

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
			a.computerBackend, a.computerTargets = backend, targets
		})
	}()
}

func (a *App) startComputer(control bool) {
	if a.computerBusy || a.computerBackend == nil || a.ctrl == nil || a.win == nil || a.computerSelected < 0 || a.computerSelected >= len(a.computerTargets) {
		return
	}
	a.computerBusy, a.computerError = true, ""
	a.computerSetupSeq++
	seq, win, backend := a.computerSetupSeq, a.win, a.computerBackend
	target, conversation := a.computerTargets[a.computerSelected], a.status.SessionID
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a.computerSetupStop = cancel
	go func() {
		session := computeruse.New(backend)
		err := session.Start(ctx, target, computeruse.Capabilities{Observe: true, Control: control})
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
				a.computerError = ctx.Err().Error()
				return
			}
			if err != nil {
				a.computerError = err.Error()
				session.Stop()
				return
			}
			a.computerBackend = nil // ownership transferred to session
			a.computerSession, a.computerPickerOpen = session, false
			a.showComputerPanel()
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
	if a.ctrl != nil {
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
}

func (a *App) startComputerPreview() {
	if a.computerPreviewStop != nil || a.computerSession == nil || a.win == nil {
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
				frameCtx, stop := context.WithTimeout(ctx, 5*time.Second)
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
			ui.Text(c, "Choose one application window. Its contents may be sent to your configured model provider when the agent requests a snapshot or screenshot. System permissions do not grant the agent authority to switch targets.").FontSize(12).TextColor(t.TextMuted).Wrap()
			ui.Text(c, fmt.Sprintf("System permissions: screen capture %s · accessibility %s", a.computerPermission.ScreenCapture, a.computerPermission.Accessibility)).FontSize(11).TextColor(t.TextMuted).Wrap()
			if a.computerError != "" {
				ui.Text(c, a.computerError).FontSize(11).TextColor(t.Warning).Wrap()
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
				}
				if ui.Button(c, "View only").Disabled(a.computerBusy || a.computerSelected < 0).Clicked() {
					a.startComputer(false)
				}
				if ui.PrimaryButton(c, "Allow control").Disabled(a.computerBusy || a.computerSelected < 0).Clicked() {
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
					ui.Text(c, "Waiting for target window preview…").FontSize(12).TextColor(t.TextMuted)
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
