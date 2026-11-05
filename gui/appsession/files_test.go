package appsession

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProjectFilesAreBoundedSortedAndDoNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"src", ".github", ".git", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside-link")); err != nil {
			t.Fatal(err)
		}
	}
	c := &Controller{cwd: root}
	entries, err := c.ListProjectFiles("")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	present := map[string]bool{}
	var symlink bool
	for _, entry := range entries {
		names = append(names, entry.Name)
		present[entry.Name] = true
		if entry.Name == "outside-link" {
			symlink = entry.Symlink
		}
	}
	if present[".git"] || present[".github"] || present["node_modules"] || !present["src"] || !present["README.md"] {
		t.Fatalf("entries=%v", names)
	}
	hidden, err := c.ListProjectFilesWithHidden("", true)
	if err != nil {
		t.Fatal(err)
	}
	foundHidden := map[string]bool{}
	for _, entry := range hidden {
		foundHidden[entry.Name] = true
	}
	if !foundHidden[".git"] || !foundHidden[".github"] || foundHidden["node_modules"] {
		t.Fatalf("show hidden entries = %v", foundHidden)
	}
	if runtime.GOOS != "windows" {
		if !symlink {
			t.Fatalf("symlink not marked: %+v", entries)
		}
		if _, err := c.ListProjectFiles("outside-link"); err == nil {
			t.Fatal("followed a directory symlink")
		}
	}
	if _, err := c.ListProjectFiles("../"); err == nil {
		t.Fatal("listed outside the project")
	}
	preview, err := c.ReadProjectFile("src/main.go")
	if err != nil || preview.Text != "package main\n" || preview.Binary {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

func TestProjectFilePreviewDetectsBinaryAndTruncates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", maxProjectPreviewBytes+100)), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cwd: root}
	binary, err := c.ReadProjectFile("binary.bin")
	if err != nil || !binary.Binary || binary.Text != "" {
		t.Fatalf("binary=%+v err=%v", binary, err)
	}
	large, err := c.ReadProjectFile("large.txt")
	if err != nil || !large.Truncated || len(large.Text) != maxProjectPreviewBytes {
		t.Fatalf("large len=%d truncated=%t err=%v", len(large.Text), large.Truncated, err)
	}
}

func TestProjectFileSaveChecksDiskChangesAndKeepsMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("old\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cwd: root, readOnly: true}
	preview, err := c.ReadProjectFile("main.go")
	if err != nil || preview.Digest == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	digest, err := c.SaveProjectFile(root, "main.go", preview.Digest, "new\r\n")
	if err != nil || digest == "" {
		t.Fatalf("operator save failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new\r\n" {
		t.Fatalf("saved=%q err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions changed: %v %v", info, err)
	}
	if _, err := c.SaveProjectFile(root, "main.go", preview.Digest, "overwrite"); err == nil {
		t.Fatal("stale preview overwrote a changed file")
	}
	if _, err := c.SaveProjectFile(root, "../outside", digest, "overwrite"); err == nil {
		t.Fatal("save escaped the project")
	}
	if _, err := c.SaveProjectFile(t.TempDir(), "main.go", digest, "overwrite"); err == nil {
		t.Fatal("save accepted a different project")
	}
}

func TestProjectFileSaveReportsOSPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX file mode does not prove denial for this user")
	}
	root := t.TempDir()
	path := filepath.Join(root, "readonly.txt")
	if err := os.WriteFile(path, []byte("readable"), 0o400); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cwd: root}
	preview, err := c.ReadProjectFile("readonly.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveProjectFile(root, "readonly.txt", preview.Digest, "changed"); err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected OS permission error, got %v", err)
	}
}
