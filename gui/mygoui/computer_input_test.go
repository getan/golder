package mygoui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/computeruse"
)

type computerManualBackend struct {
	computerUIBackend
	png                       []byte
	agentCalls, clicks, types int
	text                      string
}

func (b *computerManualBackend) Capture(context.Context, computeruse.Target) ([]byte, error) {
	return b.png, nil
}
func (b *computerManualBackend) CaptureBound(context.Context, computeruse.Target) ([]byte, string, error) {
	return b.png, "gui-frame-binding", nil
}
func (b *computerManualBackend) ClickFrame(context.Context, computeruse.Target, string, float64, float64) error {
	b.agentCalls++
	return nil
}
func (b *computerManualBackend) TypeTextFrame(context.Context, computeruse.Target, string, string) error {
	b.agentCalls++
	return nil
}
func (b *computerManualBackend) OperatorClickFrame(context.Context, computeruse.Target, string, float64, float64) error {
	b.clicks++
	return nil
}
func (b *computerManualBackend) OperatorTypeTextFrame(_ context.Context, _ computeruse.Target, _, text string) error {
	b.types++
	b.text = text
	return nil
}

func manualComputerApp(t *testing.T) (*App, *computerManualBackend) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	b := &computerManualBackend{png: data.Bytes()}
	s := computeruse.New(b)
	if err := s.Start(context.Background(), computeruse.Target{ID: "window", Application: "Test App"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a := testApp()
	a.computerOpen, a.computerSession = true, s
	a.computerFrame = ui.NewBitmap(img)
	return a, b
}

func setManualComputerFrame(t *testing.T, a *App) {
	t.Helper()
	f, err := computeruse.NewOperatorView(a.computerSession).CapturePreview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.computerFrameInfo = computeruse.Frame{ID: f.ID, Width: f.Width, Height: f.Height}
}

func TestGUIComputerTakeoverClickTypeAndResume(t *testing.T) {
	a, b := manualComputerApp(t)
	tt := ui.NewTester(a.view, 1400, 900)
	if err := tt.Click("Pause / take over"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("人工控制 / Human control") || !a.computerSession.Status().OperatorControl {
		t.Fatalf("takeover UI missing: %q", tt.Texts())
	}
	if _, err := a.computerSession.Capture(context.Background()); !errors.Is(err, computeruse.ErrPaused) {
		t.Fatalf("Agent was not blocked: %v", err)
	}
	setManualComputerFrame(t, a)
	tt.Frame()
	if err := tt.Click("Computer window preview"); err != nil {
		t.Fatal(err)
	}
	if b.clicks != 1 || b.agentCalls != 0 || a.computerFrameInfo.ID != "" {
		t.Fatalf("manual click did not use the local path: clicks=%d agent=%d error=%q", b.clicks, b.agentCalls, a.computerError)
	}
	setManualComputerFrame(t, a)
	tt.Frame()
	if err := tt.Click("Computer text input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("大明1566🙂")
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "大明1566") {
		t.Fatal("manual text was echoed into rendered labels")
	}
	if err := tt.Click("输入 / Insert"); err != nil {
		t.Fatal(err)
	}
	if b.types != 1 || b.text != "大明1566🙂" || a.computerTextDraft != "" || b.agentCalls != 0 {
		t.Fatalf("manual typing failed: types=%d error=%q", b.types, a.computerError)
	}
	if err := tt.Click("Resume"); err != nil {
		t.Fatal(err)
	}
	if a.computerSession.Status().Paused || a.computerSession.Status().OperatorControl || !tt.HasText("Agent control permitted") {
		t.Fatalf("control not handed back: %q", tt.Texts())
	}
}

func TestGUIComputerInputGuardsAndNavigationCancellation(t *testing.T) {
	a, b := manualComputerApp(t)
	setManualComputerFrame(t, a)
	event := ui.InputEvent{Kind: ui.InputPointerDown, X: 100, Y: 100}
	if a.clickComputerPreview(a.computerFrameInfo, 200, 200, event) || b.clicks != 0 {
		t.Fatal("manual click permitted during Agent control")
	}
	a.takeOverComputer()
	setManualComputerFrame(t, a)
	a.status.Permission = "read-only"
	if a.clickComputerPreview(a.computerFrameInfo, 200, 200, event) {
		t.Fatal("read-only GUI allowed input")
	}
	a.status.Permission = "auto"
	a.computerInputBusy = true
	if a.clickComputerPreview(a.computerFrameInfo, 200, 200, event) {
		t.Fatal("second input was accepted while busy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.computerInputStop, a.computerTextDraft = cancel, "private draft"
	before := a.computerSession.Status()
	a.hideComputerView()
	if ctx.Err() == nil || a.computerInputBusy || a.computerTextDraft != "" || a.computerFrameInfo.ID != "" || a.computerSession.Status() != before {
		t.Fatal("navigation did not cancel local input and clear its draft while preserving ownership")
	}
}

func TestGUIComputerPointerContainAndInvalidCoordinates(t *testing.T) {
	f := computeruse.Frame{ID: "frame", Width: 200, Height: 100}
	x, y, ok := computerPreviewPoint(f, 400, 400, 200, 200)
	if !ok || x != 100 || y != 50 {
		t.Fatalf("incorrect contain mapping: %v %v %t", x, y, ok)
	}
	for _, p := range [][2]float32{{200, 50}, {200, 350}, {400, 200}, {-1, 200}, {float32(math.NaN()), 200}} {
		if _, _, ok := computerPreviewPoint(f, 400, 400, p[0], p[1]); ok {
			t.Fatalf("outside/invalid point accepted: %v", p)
		}
	}
}
