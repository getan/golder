package mygoui

import (
	"context"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/computeruse"
)

func (a *App) cancelComputerInput() {
	a.computerInputSeq++
	if a.computerInputStop != nil {
		a.computerInputStop()
		a.computerInputStop = nil
	}
	a.computerInputBusy = false
	a.computerTextDraft = ""
	a.computerFrameInfo = computeruse.Frame{}
}

func (a *App) takeOverComputer() {
	if a.computerSession == nil || a.status.Permission == "read-only" {
		return
	}
	a.cancelComputerInput()
	if err := computeruse.NewOperatorView(a.computerSession).TakeOver(); err != nil {
		a.setComputerFailure(err)
		return
	}
	a.computerError = ""
	a.restartComputerPreview()
}

func (a *App) restartComputerPreview() {
	a.computerPreviewSeq++
	if a.computerPreviewStop != nil {
		a.computerPreviewStop()
		a.computerPreviewStop = nil
	}
	a.computerFrameInfo = computeruse.Frame{}
	if a.computerOpen {
		a.startComputerPreview()
	}
}

func (a *App) computerManualReady() bool {
	if a.computerSession == nil || !a.computerOpen || a.computerInputBusy || a.status.Permission == "read-only" || a.computerFrameInfo.ID == "" {
		return false
	}
	s := a.computerSession.Status()
	return s.Active && s.Paused && s.OperatorControl && s.Capabilities.Control
}

// MyGo's Contain image is centered and aspect-preserving. Letterboxing must
// never map to the target; pointer coordinates are local DIPs, not screen pixels.
func computerPreviewPoint(frame computeruse.Frame, width, height, x, y float32) (float64, float64, bool) {
	if frame.ID == "" || frame.Width <= 0 || frame.Height <= 0 || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	for _, value := range []float32{width, height, x, y} {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return 0, 0, false
		}
	}
	scale := min(float64(width)/float64(frame.Width), float64(height)/float64(frame.Height))
	w, h := float64(frame.Width)*scale, float64(frame.Height)*scale
	left, top := (float64(width)-w)/2, (float64(height)-h)/2
	if float64(x) < left || float64(x) >= left+w || float64(y) < top || float64(y) >= top+h {
		return 0, 0, false
	}
	return (float64(x) - left) / scale, (float64(y) - top) / scale, true
}

func (a *App) clickComputerPreview(frame computeruse.Frame, width, height float32, event ui.InputEvent) bool {
	if event.Kind != ui.InputPointerDown || event.Button != 0 || !a.computerManualReady() || a.computerFrameInfo.ID != frame.ID {
		return false
	}
	x, y, ok := computerPreviewPoint(frame, width, height, event.X, event.Y)
	if !ok {
		return false
	}
	a.runComputerInput(func(ctx context.Context, view *computeruse.OperatorView, id string) error {
		return view.ClickFrame(ctx, id, x, y)
	})
	return true
}

func (a *App) typeComputerText() {
	if !a.computerManualReady() || a.computerTextDraft == "" {
		return
	}
	text := a.computerTextDraft
	a.computerTextDraft = ""
	a.runComputerInput(func(ctx context.Context, view *computeruse.OperatorView, id string) error {
		return view.TypeTextFrame(ctx, id, text)
	})
}

func (a *App) runComputerInput(action func(context.Context, *computeruse.OperatorView, string) error) {
	if !a.computerManualReady() {
		return
	}
	session, frameID := a.computerSession, a.computerFrameInfo.ID
	a.computerFrameInfo = computeruse.Frame{} // one-use lease, consumed before dispatch
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a.computerInputSeq++
	seq := a.computerInputSeq
	a.computerInputStop, a.computerInputBusy = cancel, true
	finish := func(err error) {
		if a.computerSession != session || a.computerInputSeq != seq {
			return
		}
		a.computerInputStop, a.computerInputBusy = nil, false
		if err != nil {
			a.setComputerFailure(err)
		} else {
			a.computerError = ""
		}
	}
	run := func() error {
		defer cancel()
		return action(ctx, computeruse.NewOperatorView(session), frameID)
	}
	if a.win == nil { // Headless UI uses the same operation path synchronously.
		finish(run())
		return
	}
	win := a.win
	go func() {
		err := run()
		win.Update(func() { finish(err) })
	}()
}
