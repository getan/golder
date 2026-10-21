package run

import (
	"strings"
	"testing"
)

func TestBuiltinToolsAdvertiseTuiosOnlyInAPane(t *testing.T) {
	t.Setenv("TUIOS_SOCKET", "")
	t.Setenv("TUIOS_SESSION", "")
	for _, tool := range BuiltinTools(t.TempDir(), false) {
		if strings.HasPrefix(tool.Name(), "tuios_") {
			t.Fatalf("tuios tool advertised outside tuios: %s", tool.Name())
		}
	}

	t.Setenv("TUIOS_SOCKET", "/tmp/tuios.sock")
	t.Setenv("TUIOS_SESSION", "work")
	found := false
	for _, tool := range BuiltinTools(t.TempDir(), false) {
		if tool.Name() == "tuios_run" {
			found = true
		}
	}
	if !found {
		t.Fatal("tuios_run was not advertised inside a tuios pane")
	}
}

func TestChildToolSetKeepsTuiosReadsButNotPaneWrites(t *testing.T) {
	t.Setenv("TUIOS_SOCKET", "/tmp/tuios.sock")
	t.Setenv("TUIOS_SESSION", "work")
	read, write := false, false
	for _, tool := range ChildToolSet(t.TempDir(), ToolPolicy{}) {
		switch tool.Name() {
		case "tuios_list_windows", "tuios_capture_pane", "tuios_wait_for":
			read = true
		case "tuios_run", "tuios_window":
			write = true
		}
	}
	if !read {
		t.Fatal("child should inherit tuios read tools")
	}
	if write {
		t.Fatal("child must not inherit tuios_run/tuios_window by default")
	}
}
