package plugin

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/diagtrace"
)

// The startup trace must name a slow plugin without exposing where the person
// keeps it: only the executable's basename may appear.
func TestDiscoverTraceRecordsOnlyBasenames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad plugin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "trace.log")
	if err := diagtrace.Init(tracePath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(diagtrace.Close)
	if _, err := Discover(dir, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	diagtrace.Close()
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "plugin load name=bad plugin ok=false") {
		t.Fatalf("trace missing the failing plugin's basename:\n%s", text)
	}
	if strings.Contains(text, dir) {
		t.Fatalf("trace exposed the plugin directory:\n%s", text)
	}
}
