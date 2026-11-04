package appsession

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/getan/golder/internal/golderhome"
)

func TestDisplaySettingsDefaultAndRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv(golderhome.EnvHome, home)
	defaults, err := LoadDisplaySettings()
	if err != nil || defaults.Theme != "system" || defaults.FontScale <= 1 {
		t.Fatalf("defaults=%+v err=%v", defaults, err)
	}
	want := DisplaySettings{Theme: "dark", FontScale: 1.2}
	if err := SaveDisplaySettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDisplaySettings()
	if err != nil || got.Theme != "dark" || math.Abs(float64(got.FontScale-1.2)) > .001 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(home, golderhome.GUIDisplayFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("display settings mode=%v", info.Mode())
	}
}

func TestDisplaySettingsRejectInvalidValues(t *testing.T) {
	t.Setenv(golderhome.EnvHome, t.TempDir())
	for _, settings := range []DisplaySettings{{Theme: "sepia", FontScale: 1}, {Theme: "light", FontScale: 2}} {
		if err := SaveDisplaySettings(settings); err == nil {
			t.Fatalf("accepted %+v", settings)
		}
	}
}
