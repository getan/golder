package mygoui

import (
	"context"

	"github.com/getan/golder/internal/selfupdate"
)

// All App state is read on the UI thread, including each timer callback.
// The monitor only checks metadata; downloads still require a user action.
func (a *App) startGUIUpdateMonitor() {
	if a.updateMonitorCancel != nil || a.win == nil || a.ctrl == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.updateMonitorCancel = cancel
	a.checkGUIUpdate(false)
	win := a.win
	go func() {
		selfupdate.Monitor(ctx, func() {
			win.Update(func() {
				if ctx.Err() == nil {
					a.checkGUIUpdate(false)
				}
			})
		})
	}()
}

func (a *App) stopGUIUpdateMonitor() {
	if a.updateMonitorCancel != nil {
		a.updateMonitorCancel()
		a.updateMonitorCancel = nil
	}
}

func (a *App) guiUpdateCheckAllowed(manual bool) bool {
	// Do not overlap a check/download or replace a staged installer while the
	// person is deciding whether to open it. Manual actions remain available.
	return !a.updateBusy && (manual || (a.updatePath == "" && !a.updateDialogOpen))
}
