package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
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

// A pane can start a browser with workspace create, so the browser tool is
// advertised there even before one exists; its schema offers only list until
// the socket appears.
func TestBuiltinToolsAdvertiseBrowserBeforeOneExists(t *testing.T) {
	t.Setenv("TUIOS_SOCKET", "/tmp/tuios.sock")
	t.Setenv("TUIOS_SESSION", "work")
	t.Setenv("TUIOS_ENV", "1")
	t.Setenv("TUIOS_PANE_ID", "w1")
	t.Setenv("KITTY_BROWSE_SOCKET", "")

	// The tool is only advertised where a browser can actually be started, so
	// the test provides one on PATH instead of depending on the machine.
	dir := t.TempDir()
	bin := filepath.Join(dir, "kitty-browse")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var browser agentcore.AgentTool
	for _, tool := range BuiltinTools(t.TempDir(), false) {
		if tool.Name() == "browser" {
			browser = tool
		}
	}
	if browser == nil {
		t.Fatal("browser was not advertised inside a pane")
	}
	if schema := string(browser.Schema()); !strings.Contains(schema, `["open","list","attach"]`) {
		t.Errorf("browser schema = %s, want open/list/attach before selection", schema)
	}
}
