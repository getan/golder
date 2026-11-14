package agenttool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBrowserUploadFiles(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(browserUploadMaxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = big.Close()

	got, err := resolveBrowserUploadFiles(root, []string{"a.txt"})
	if err != nil || len(got) != 1 || !strings.HasSuffix(got[0], "a.txt") {
		t.Fatalf("valid upload = %q, %v", got, err)
	}
	if !filepath.IsAbs(got[0]) {
		t.Fatalf("resolved path is not absolute: %q", got[0])
	}

	for name, files := range map[string][]string{
		"empty":          {},
		"absolute":       {filepath.Join(root, "a.txt")},
		"parent escape":  {"../" + filepath.Base(outside) + "/secret.txt"},
		"symlink escape": {"link.txt"},
		"directory":      {"dir"},
		"missing":        {"nope.txt"},
		"oversized":      {"big.bin"},
	} {
		if _, err := resolveBrowserUploadFiles(root, files); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
