package mygoui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGUIPackageURL(t *testing.T) {
	var release guiRelease
	err := json.Unmarshal([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"golder-gui-1.2.3-arm64.dmg"},{"name":"golder-gui-setup-1.2.3-amd64.exe"},{"name":"golder-gui-1.2.3-linux-amd64.tar.gz"}]}`), &release)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ os, arch, current, suffix string }{
		{"darwin", "arm64", "1.2.2", ".dmg"},
		{"windows", "amd64", "1.2.2", ".exe"},
		{"linux", "amd64", "1.2.2", ".tar.gz"},
		{"linux", "arm64", "1.2.2", ""},
		{"windows", "arm64", "1.2.2", ""},
		{"darwin", "arm64", "1.2.3", ""},
		{"darwin", "arm64", "dev", ""},
	} {
		got := guiPackageURL(release, tc.current, tc.os, tc.arch)
		if tc.suffix == "" && got != "" || tc.suffix != "" && !strings.HasSuffix(got, tc.suffix) {
			t.Errorf("%s/%s current %s: got %q", tc.os, tc.arch, tc.current, got)
		}
	}
}
