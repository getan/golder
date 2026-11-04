package mygoui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/golderhome"
)

func TestDisplayPreferencesApplyThemeAndZoom(t *testing.T) {
	a := testApp()
	a.display = appsession.DisplaySettings{Theme: "dark", FontScale: 1.2}
	var dark bool
	var zoom float32
	tt := ui.NewTester(func(c *ui.Context) {
		a.applyDisplayPreferences(c)
		dark, zoom = c.Theme().Dark, c.Zoom()
		ui.Text(c, "preview")
	}, 400, 200)
	if !dark || math.Abs(float64(zoom-1.2)) > .001 {
		t.Fatalf("dark=%t zoom=%v", dark, zoom)
	}
	a.display = appsession.DisplaySettings{Theme: "system", FontScale: 1.06}
	tt.Frame()
	if dark || math.Abs(float64(zoom-1.06)) > .001 {
		t.Fatalf("system theme did not restore desktop appearance: dark=%t zoom=%v", dark, zoom)
	}
}

func TestDisplaySettingsPageChangesAndPersistsTheme(t *testing.T) {
	t.Setenv(golderhome.EnvHome, t.TempDir())
	a := testApp()
	a.settingsOpen, a.settingsTab = true, 3
	tt := ui.NewTester(a.view, 1000, 760)
	for _, want := range []string{"Display", "System", "Light", "Dark", "Font size", "Default", "Preview"} {
		if !tt.HasText(want) {
			t.Fatalf("Display settings missing %q; texts=%q", want, tt.Texts())
		}
	}
	if err := tt.Click("Dark"); err != nil {
		t.Fatal(err)
	}
	stored, err := appsession.LoadDisplaySettings()
	if err != nil || a.display.Theme != "dark" || stored.Theme != "dark" {
		t.Fatalf("state=%+v stored=%+v err=%v", a.display, stored, err)
	}
}
