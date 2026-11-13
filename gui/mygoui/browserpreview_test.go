package mygoui

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBrowserViewportPointAccountsForContainLetterbox(t *testing.T) {
	state := &browserWindowState{frameW: 800, frameH: 600, viewW: 1000, viewH: 600}
	x, y, ok := browserViewportPoint(state, 500, 300)
	if !ok || math.Abs(x-400) > 0.01 || math.Abs(y-300) > 0.01 {
		t.Fatalf("center = (%v,%v,%v)", x, y, ok)
	}
	if _, _, ok := browserViewportPoint(state, 10, 300); ok {
		t.Fatal("left letterbox was treated as page content")
	}
	x, y, ok = browserViewportPointClamped(state, 10, 700)
	if !ok || math.Abs(x) > 0.01 || math.Abs(y-600) > 0.01 {
		t.Fatalf("clamped release = (%v,%v,%v)", x, y, ok)
	}
}

func TestBrowserKeyMapsEditingKeys(t *testing.T) {
	for key, want := range map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyBackspace: "Backspace", ui.KeyLeft: "ArrowLeft", ui.KeyTab: "Tab",
	} {
		if got := browserKey(key); got != want {
			t.Errorf("browserKey(%v) = %q, want %q", key, got, want)
		}
	}
}

func TestBrowserCaptureFailureIsTransientAndRecovers(t *testing.T) {
	state := &browserWindowState{frameW: 10, frameH: 10}
	failed := errors.New("screenshot server returned 500 Internal Server Error: capture failed")
	state.applyFrameResult(failed, nil)
	if state.err != "" || state.frameW != 0 || state.frameH != 0 {
		t.Fatal("first transient capture failure should disable stale frame without flashing an error")
	}
	state.applyFrameResult(failed, nil)
	state.applyFrameResult(failed, nil)
	if !strings.Contains(state.err, "Browser preview unavailable") {
		t.Fatal("persistent capture failure was hidden")
	}
	state.applyFrameResult(nil, nil)
	if state.err != "" || state.frameFailures != 0 {
		t.Fatal("successful frame did not clear the stale warning")
	}
	state.applyFrameResult(nil, errors.New("input failed"))
	if state.err != "input failed" {
		t.Fatal("input error was not independently reported")
	}
}

func TestToolTTYIDNeedsTTYAndStructuredBashID(t *testing.T) {
	details := map[string]any{"bash_id": "bash_7"}
	if got := toolTTYID(details, map[string]any{"tty": true}); got != "bash_7" {
		t.Fatalf("tty id = %q", got)
	}
	if got := toolTTYID(details, map[string]any{"tty": false}); got != "" {
		t.Fatalf("pipe command exposed terminal id %q", got)
	}
}
