package appsession

import (
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
	if present[".git"] || present["node_modules"] || !present[".github"] || !present["src"] || !present["README.md"] {
		t.Fatalf("entries=%v", names)
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
