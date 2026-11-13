package appsession

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/session"
)

func TestManualFileBrowserCanNavigateWithoutGrantingAgentPaths(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("manual"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cwd: project, header: session.SessionHeader{ID: "conversation"}}
	browser, err := c.FileBrowserAt("conversation", project, parent)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := browser.Read("outside.txt")
	if err != nil || preview.Text != "manual" {
		t.Fatalf("manual read: %+v %v", preview, err)
	}
	if _, err := browser.Save("outside.txt", preview.Digest, "edited"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadProjectFile("../outside.txt"); err == nil {
		t.Fatal("manual navigation broadened project API")
	}
	c.header.ID = "different"
	if _, err := browser.Read("outside.txt"); err == nil {
		t.Fatal("stale conversation browser remained usable")
	}
}
