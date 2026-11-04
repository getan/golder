package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
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

func TestComposerShowsReadOnlySessionOwner(t *testing.T) {
	a := testApp()
	a.status.ReadOnly = true
	a.status.Writer = appsession.SessionWriterView{Frontend: "TUI", PID: 4242}
	tt := ui.NewTester(a.view, 1000, 700)
	for _, want := range []string{"Read-only · active in TUI (PID 4242)", "Send message"} {
		if !tt.HasText(want) {
			t.Fatalf("read-only composer missing %q; texts=%q", want, tt.Texts())
		}
	}
}
