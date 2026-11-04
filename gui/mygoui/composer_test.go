package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSendAndStopButtonsUseNeutralPalette(t *testing.T) {
	theme := ui.LightTheme()
	face, _, _, text := composerControlPalette(theme, false, true)
	if face != theme.SurfacePressed || text != theme.Text || face == theme.Accent || face == theme.Danger {
		t.Fatalf("ready send palette face=%v text=%v", face, text)
	}
	face, _, _, text = composerControlPalette(theme, true, true)
	if face != theme.SurfacePressed || text != theme.Text || face == theme.Danger {
		t.Fatalf("stop palette face=%v text=%v", face, text)
	}
	face, _, _, text = composerControlPalette(theme, false, false)
	if face != theme.Surface || text != theme.TextMuted {
		t.Fatalf("disabled send palette face=%v text=%v", face, text)
	}
}
