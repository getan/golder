package run

import (
	"strings"
	"testing"
)

func TestBuiltinToolsAdvertiseTuiosOnlyInAPane(t *testing.T) {
	t.Setenv("TUIOS_SOCKET", "")
	t.Setenv("TUIOS_SESSION", "")
	t.Setenv("TUIOS_ENV", "")
	t.Setenv("TUIOS_PANE_ID", "")
	for _, tool := range BuiltinTools(t.TempDir(), false) {
		if tool.Name() == "tuios" {
			t.Fatalf("tuios tool advertised outside tuios: %s", tool.Name())
		}
	}

	t.Setenv("TUIOS_SOCKET", "/tmp/tuios.sock")
	t.Setenv("TUIOS_SESSION", "work")
	t.Setenv("TUIOS_ENV", "1")
	t.Setenv("TUIOS_PANE_ID", "w1")
	found := false
	for _, tool := range BuiltinTools(t.TempDir(), false) {
		if tool.Name() == "tuios" {
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
	t.Setenv("TUIOS_ENV", "1")
	t.Setenv("TUIOS_PANE_ID", "w1")
	read, write := false, false
	for _, tool := range ChildToolSet(t.TempDir(), ToolPolicy{}) {
		if tool.Name() != "tuios" {
			continue
		}
		read = strings.Contains(string(tool.Schema()), `"capture"`)
		write = strings.Contains(string(tool.Schema()), `"run"`)
	}
	if !read {
		t.Fatal("child should inherit tuios read actions")
	}
	if write {
		t.Fatal("child schema must not advertise control actions")
	}
}
