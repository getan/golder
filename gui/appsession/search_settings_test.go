package appsession

import (
	"testing"

	"github.com/getan/golder/internal/agenttool"
)

func TestSearchSettingsRoundTrip(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Cleanup(func() { agenttool.SetSearchEnv(nil) })

	want := SearchSettings{Tavily: "tvly", Exa: "exa", DeepSeek: "dsk"}
	if _, err := SaveSearchSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSearchSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if _, err := SaveSearchSettings(SearchSettings{}); err != nil {
		t.Fatal(err)
	}
	empty, err := LoadSearchSettings()
	if err != nil || empty != (SearchSettings{}) {
		t.Fatalf("cleared settings = %+v, err=%v", empty, err)
	}
}
