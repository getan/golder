//go:build windows

package mygoui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGUIInternetDownloadHasInternetZone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.part")
	if err := os.WriteFile(path, []byte("download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markGUIInternetDownload(path, "https://github.com/getan/golder"); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(dir, "download.exe")
	if err := os.Link(path, published); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(published + ":Zone.Identifier")
	if err != nil || !strings.Contains(string(data), "ZoneId=3") {
		t.Fatalf("missing internet zone: %q, %v", data, err)
	}
}
