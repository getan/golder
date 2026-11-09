package mygoui

import (
	"context"
	"runtime"
	"time"

	"github.com/egoist/mygo"
)

// Keep AppKit's application menu on macOS. On Windows AppMenu is a mac-only
// role and is omitted by MyGo; provide a real File/Exit entry instead. Route
// Exit through Window.Close so it observes Golder's terminal/dialog guards,
// rather than stopping the native message loop from an active modal callback.
func desktopMenuItems(goos string, closeWindow func()) []*mygo.MenuItem {
	if goos == "windows" {
		return []*mygo.MenuItem{
			{Label: "File", Submenu: []*mygo.MenuItem{
				{Label: "Exit", Click: func(*mygo.MenuItem, *mygo.Window) { closeWindow() }},
			}},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleWindowMenu},
		}
	}
	return []*mygo.MenuItem{{Role: mygo.RoleAppMenu}, {Role: mygo.RoleEditMenu}, {Role: mygo.RoleWindowMenu}}
}

// fileDialogState belongs to the UI thread. Windows file pickers run nested
// native message loops: do not tear down their owner or start another picker
// while one is outstanding. Complete a pending close after the picker returns.
type fileDialogState struct {
	active       bool
	closePending bool
	generation   uint64
}

func (s *fileDialogState) begin() bool {
	if s.active || s.closePending {
		return false
	}
	s.active = true
	s.generation++
	return true
}

func (s *fileDialogState) deferClose() bool {
	if !s.active {
		return false
	}
	s.closePending = true
	return true
}

func (s *fileDialogState) finish() bool {
	close := s.closePending
	s.active, s.closePending = false, false
	return close
}

// beginFileDialog must be called on the UI thread before starting a worker.
// complete marshals the result back and drops it when shutdown was requested.
// Other platforms retain their existing sheet/picker lifecycle.
func (a *App) beginFileDialog() (win *mygo.Window, complete func(func()), ok bool) {
	win = a.win
	if win == nil || a.terminalShuttingDown {
		return nil, nil, false
	}
	windows := runtime.GOOS == "windows"
	if windows && !a.fileDialog.begin() {
		a.banner, a.bannerErr = "Finish the open file dialog first.", false
		win.Invalidate()
		return nil, nil, false
	}
	return win, func(apply func()) {
		win.Update(func() {
			if windows && a.fileDialog.finish() {
				win.Close()
				return
			}
			if !a.terminalShuttingDown {
				apply()
			}
		})
	}, true
}

func (a *App) deferCloseForFileDialog() bool {
	alreadyPending := a.fileDialog.closePending
	if runtime.GOOS != "windows" || !a.fileDialog.deferClose() {
		return false
	}
	a.banner, a.bannerErr = "Closing the file dialog…", false
	a.win.Invalidate()
	if !alreadyPending {
		a.cancelPendingFileDialog()
	}
	return true
}

// The close request can race the worker posting Dialog.Open. A bounded retry
// covers that interval; generation matching prevents a late callback from
// touching a later picker. Only its owner thread's native common dialog is
// canceled, never a global Escape, another app or the main window itself.
func (a *App) cancelPendingFileDialog() {
	win, generation := a.win, a.fileDialog.generation
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			result := make(chan bool, 1)
			win.Update(func() {
				if ctx.Err() != nil || a.fileDialog.generation != generation || !a.fileDialog.active || !a.fileDialog.closePending {
					result <- true
					return
				}
				result <- dismissOwnedFileDialog(win.NativeHandle())
			})
			select {
			case done := <-result:
				if done {
					return
				}
			case <-ctx.Done():
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
