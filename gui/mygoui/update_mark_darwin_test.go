//go:build darwin

package mygoui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGUIInternetDownloadHasQuarantine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.part")
	if err := os.WriteFile(path, []byte("download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markGUIInternetDownload(path, "https://github.com/getan/golder"); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(dir, "download.dmg")
	if err := os.Link(path, published); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, err := unix.Getxattr(published, "com.apple.quarantine", buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(buf[:n]), "0081;") {
		t.Fatalf("missing quarantine: %q", buf[:n])
	}
}
